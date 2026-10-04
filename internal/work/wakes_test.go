//go:build !windows

package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

func TestFiredPRChecksWakeCapturesDurableCIInput(t *testing.T) {
	a := testLoop(t)
	state := "PENDING"
	a.github = github.Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		return syntheticWakePR(state, "", args...)
	}}
	call(t, a, "wake_me_when", map[string]string{"on": "pr_checks", "target": "example/repo#7", "match": "SUCCESS", "prompt": "Check this", "timeout": "1h"})
	state = "SUCCESS"
	if err := a.checkWakes(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	c := core.NewAutopilotCoordinator(a.Core, nil)
	f, err := c.Register("ci-failures", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	f.OnEvents([]string{"ci.result"}, func(_ context.Context, _ core.Snapshot, e core.AutopilotEvent) ([]core.EventProposal, error) {
		seen++
		var ci core.CIEvent
		if err := json.Unmarshal(e.Payload, &ci); err != nil {
			t.Fatal(err)
		}
		if ci.Commit != "0123456789abcdef" || ci.Repo != "example/repo" || ci.State != "SUCCESS" {
			t.Fatalf("CI identity lost: %+v", ci)
		}
		return nil, nil
	})
	for i := 0; i < 2; i++ {
		if err := c.ConsumeEvents(t.Context(), time.Now(), func() bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	if seen != 1 {
		t.Fatalf("CI wake captured %d times", seen)
	}
}

// call drives the wake tools the way the assistant does.
func call(t *testing.T, a *Loop, tool string, in map[string]string) any {
	t.Helper()
	ctx := context.Background()
	var out any
	var err error
	switch tool {
	case "wake_me_when":
		out, err = a.WakeMeWhen(ctx, WakeRequest{On: in["on"], ProjectID: in["project_id"], Target: in["target"], Match: in["match"], Prompt: in["prompt"], Timeout: in["timeout"]})
	case "list_wakes":
		out, err = a.OpenWakes(ctx)
	case "cancel_wake":
		out, err = a.Core.CancelWake(ctx, in["handle"], "")
	}
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func TestTheAssistantWaitsOnSeveralThingsAndCancelsWhatItNoLongerNeeds(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	ctx := context.Background()
	repo := t.TempDir()
	ownerGit(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0600)
	ownerGit(t, repo, "add", "-A")
	ownerGit(t, repo, "commit", "-q", "-m", "start")
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Repo", Directories: []string{repo}, Brief: core.BriefInput{Goal: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	soon := call(t, a, "wake_me_when", map[string]string{"on": "time", "project_id": "", "target": "1m", "match": "", "prompt": "look at the build", "timeout": ""}).(core.Wake)
	branch := call(t, a, "wake_me_when", map[string]string{"on": "branch", "project_id": p.ID, "target": "main", "match": "", "prompt": "main moved; rebase the notes", "timeout": "2h"}).(core.Wake)
	later := call(t, a, "wake_me_when", map[string]string{"on": "time", "project_id": "", "target": "3h", "match": "", "prompt": "never mind", "timeout": ""}).(core.Wake)
	if open := call(t, a, "list_wakes", map[string]string{}).([]core.Wake); len(open) != 3 {
		t.Fatalf("open wakes %+v", open)
	}
	call(t, a, "cancel_wake", map[string]string{"handle": later.ID})

	now := time.Now()
	if err = a.checkWakes(ctx, now); err != nil {
		t.Fatal(err)
	}
	if open := call(t, a, "list_wakes", map[string]string{}).([]core.Wake); len(open) != 2 {
		t.Fatalf("something fired early or the cancel was lost: %+v", open)
	}
	os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b"), 0600)
	ownerGit(t, repo, "add", "-A")
	ownerGit(t, repo, "commit", "-q", "-m", "moved")
	if err = a.checkWakes(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	fired := map[string]core.Wake{}
	for _, w := range snap.Wakes {
		fired[w.ID] = w
	}
	if fired[soon.ID].Status != core.WakeFired || fired[branch.ID].Status != core.WakeFired || fired[later.ID].Status != core.WakeCancelled {
		t.Fatalf("wakes %+v", fired)
	}
	if !strings.Contains(fired[branch.ID].Event, "main moved from") {
		t.Fatalf("branch event %q", fired[branch.ID].Event)
	}
	turns, _ := a.Core.ChatTurns(ctx)
	var wakeTurn core.ChatTurn
	for _, turn := range turns {
		if turn.Origin == core.OriginWake && turn.Status == "queued" {
			wakeTurn = turn
		}
	}
	if len(wakeTurn.WakeIDs) != 2 {
		t.Fatalf("both wake-ups should arrive in one turn: %+v", wakeTurn)
	}

	// A wake nothing answers still comes, saying it timed out.
	quiet := call(t, a, "wake_me_when", map[string]string{"on": "branch", "project_id": p.ID, "target": "main", "match": "", "prompt": "", "timeout": "10m"}).(core.Wake)
	if err = a.checkWakes(ctx, now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	for _, w := range snap.Wakes {
		if w.ID == quiet.ID && (w.Status != core.WakeFired || !w.TimedOut) {
			t.Fatalf("a wake that timed out was not delivered: %+v", w)
		}
	}
	// Waiting on a pull request starts from what GitHub shows now.
	remote := t.TempDir()
	ownerGit(t, remote, "init", "-q", "--bare", "-b", "main")
	ownerGit(t, repo, "push", "-q", remote, "main:paul/x")
	gh := &fakeGitHub{t: t, remote: remote, head: "paul/x", opened: 1, checks: "PENDING"}
	a.github = github.Client{Run: gh.run}
	checks := call(t, a, "wake_me_when", map[string]string{"on": "pr_checks", "project_id": "", "target": "o/r#7", "match": "SUCCESS", "prompt": "merge it", "timeout": ""}).(core.Wake)
	if !strings.HasPrefix(checks.Baseline, "PENDING@") {
		t.Fatalf("baseline %q", checks.Baseline)
	}
}

func syntheticWakePR(state, url string, args ...string) ([]byte, error) {
	if len(args) > 1 && args[0] == "api" && args[1] == "graphql" {
		return []byte(`{"data":{"repository":{"pullRequest":{"state":"OPEN","headRefOid":"0123456789abcdef","reviewThreads":{"nodes":[]}}}}}`), nil
	}
	return json.Marshal(github.PR{HeadRefOid: "0123456789abcdef", State: "OPEN", URL: url, Checks: []github.Check{{Status: "COMPLETED", Conclusion: state}}})
}

func TestCICaptureFailureDoesNotChangeWakeFiring(t *testing.T) {
	for _, storageFailure := range []bool{false, true} {
		name := "validation"
		if storageFailure {
			name = "storage"
		}
		t.Run(name, func(t *testing.T) {
			a := testLoop(t)
			state := "PENDING"
			url := strings.Repeat("x", 2049)
			if storageFailure {
				url = ""
				db, err := sql.Open("sqlite", filepath.Join(a.Core.StateDirectory(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`CREATE TRIGGER fail_ci BEFORE INSERT ON autopilot_events WHEN NEW.kind='ci.result' BEGIN SELECT RAISE(ABORT,'synthetic capture failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			a.github = github.Client{Run: func(_ context.Context, args ...string) ([]byte, error) { return syntheticWakePR(state, url, args...) }}
			for _, target := range []string{"example/repo#7", "example/repo#8"} {
				call(t, a, "wake_me_when", map[string]string{"on": "pr_checks", "target": target, "match": "SUCCESS", "prompt": "Check", "timeout": "1h"})
			}
			state = "SUCCESS"
			if err := a.checkWakes(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			snap, err := a.Core.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			fired := 0
			for _, w := range snap.Wakes {
				if w.Status == core.WakeFired {
					fired++
				}
			}
			if fired != 2 {
				t.Fatal("capture failure prevented wakes firing", fired)
			}
			reported := 0
			for _, activity := range snap.Activity {
				if activity.Kind == "autopilot.ci.capture_failed" {
					reported++
				}
			}
			if reported != 2 {
				t.Fatal("capture failure not reported", reported)
			}
		})
	}
}
