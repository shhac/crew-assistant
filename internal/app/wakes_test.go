//go:build !windows

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

func call(t *testing.T, a *App, tool string, in any) any {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := a.Execute(context.Background(), tool, raw)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func TestTheAssistantWaitsOnSeveralThingsAndCancelsWhatItNoLongerNeeds(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	repo := t.TempDir()
	ownerGit(t, repo, "init", "-q", "-b", "main")
	ownerGit(t, repo, "config", "commit.gpgsign", "false")
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
	raw, _ := json.Marshal(map[string]string{"on": "pr_checks", "project_id": "", "target": "shhac/x#1", "match": "", "prompt": "", "timeout": ""})
	if _, err = a.Execute(ctx, "wake_me_when", raw); err == nil {
		t.Fatal("waiting on a pull request was accepted before it is built")
	}
}
