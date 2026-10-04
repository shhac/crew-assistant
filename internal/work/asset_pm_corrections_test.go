//go:build !windows

package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func pmAssetDraft(t *testing.T) (pmPush, core.Project, core.Task) {
	t.Helper()
	w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
	ctx := context.Background()
	task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Assets", Criteria: []string{"Frames", "Icons"}})
	if err != nil {
		t.Fatal(err)
	}
	seatDesigner(t, w.a, w.p.ID)
	task = stepUntil(t, w.a, task.ID, func(task core.Task) bool { return len(task.Revisions) > 0 })
	snap, _ := w.a.Core.Snapshot(ctx)
	p, _ := findProject(snap, w.p.ID)
	task, err = w.a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Status = core.TaskDeciding
		for _, checker := range task.Checkers() {
			task.Verdicts = append(task.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, TextVersion: task.TextVersion, Outcome: core.VerdictPass})
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, p, task
}

func TestNullNestedLandingObstacleStillReadsTopLevel(t *testing.T) {
	w, p, task := pmAssetDraft(t)
	w.runner.pmLand = []string{`{"land":true,"reason":"generator unavailable","blocked_asset":null,"asset_creation":true,"requirement":"Frames"}`}
	m, _ := w.a.mediumFor(context.Background(), p, task.Playbook)
	if err := w.a.pmLanding(context.Background(), p, task, task.Revisions[0], m); err != nil {
		t.Fatal(err)
	}
	snap, _ := w.a.Core.Snapshot(context.Background())
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskWriting || got.Approved != 0 || got.LandDecision != nil || got.DecisionID != "" || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Frames" || !got.Unreachable[0].NeedsAssetReply() {
		t.Fatal("null nested field hid the top-level obstacle", got)
	}
}

func TestMalformedNestedObstacleRetainsTopLevelAcrossCorrectionAndRestart(t *testing.T) {
	for _, nested := range []string{`{}`, `"invalid"`, `{"requirement":45,"why":"unreadable","report_id":"unknown","asset_creation":false}`} {
		t.Run(nested, func(t *testing.T) {
			w, p, task := pmAssetDraft(t)
			ctx := context.Background()
			first := fmt.Sprintf(`{"land":true,"reason":"generator unavailable","blocked_asset":%s,"requirement":"Frames","why":"generator unavailable","asset_creation":true}`, nested)
			second := `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Icons","why":"code only","asset_creation":false}}`
			m, _ := w.a.mediumFor(ctx, p, task.Playbook)
			w.runner.pmLand = []string{first, second}
			if err := w.a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
				t.Fatal(err)
			}
			got := taskByID(t, w.a, task.ID)
			if got.Status != core.TaskWriting || got.Approved != 0 || got.DecisionID != "" || got.LandDecision != nil || !got.NeedsLandingAssetReply() {
				t.Fatal("unrelated correction erased Frames", got)
			}
			// Recreate a stop immediately after the first observation is retained,
			// before either correction or routing, then use normal scheduling.
			w, p, task = pmAssetDraft(t)
			u, err := parseLandingAsset(p, task, first)
			if err != nil || u == nil || u.Criterion != "Frames" || u.AssetCreation == nil || !*u.AssetCreation {
				t.Fatal("readable evidence lost", u, err)
			}
			if _, err := w.a.Core.RetainLandingAssets(ctx, task, []core.Unreachable{*u}, "PM", false, p.Brief.Version); err != nil {
				t.Fatal(err)
			}
			w.a = restart(t, w.a)
			claims, err := w.a.Core.Schedule(ctx, func(core.Role) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			got = taskByID(t, w.a, task.ID)
			if got.Status != core.TaskWriting || got.Approved != 0 || got.DecisionID != "" || got.LandDecision != nil || len(got.Unreachable) != 1 || got.Unreachable[0].ID != u.ID || !got.Unreachable[0].NeedsAssetReply() {
				t.Fatal("restart lost production routing", got)
			}
			for _, c := range claims {
				if err := w.a.Core.ReleaseClaim(ctx, c.Task.ID, c.Claim.Token); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestUnclassifiedTopLevelPMObstacleSurvivesCorrectionAndRestart(t *testing.T) {
	w, p, task := pmAssetDraft(t)
	w.runner.pmLand = []string{
		`{"land":true,"reason":"generator unavailable","blocked_asset":null,"requirement":"Frames","why":"generator unavailable"}`,
		`{"land":true,"reason":"ready","blocked_asset":{"requirement":"Icons","why":"code only","asset_creation":false}}`,
	}
	m, _ := w.a.mediumFor(context.Background(), p, task.Playbook)
	if err := w.a.pmLanding(context.Background(), p, task, task.Revisions[0], m); err != nil {
		t.Fatal(err)
	}
	w.a = restart(t, w.a)
	got := taskByID(t, w.a, task.ID)
	if got.Status != core.TaskWriting || got.Approved != 0 || got.DecisionID != "" || got.LandDecision != nil {
		t.Fatal("unclassified evidence permitted landing", got)
	}
	for _, u := range got.Unreachable {
		if u.Criterion == "Frames" && u.ID != "" && u.AssetCreation == nil && u.NeedsAssetReply() && u.Finding != "" {
			return
		}
	}
	t.Fatal("readable unknown Frames evidence was lost", got.Unreachable)
}

func TestMatchingPMFalseCorrectionResumesLandingAfterRestart(t *testing.T) {
	for _, rejectDecision := range []bool{false, true} {
		t.Run(fmt.Sprint(rejectDecision), func(t *testing.T) {
			w, p, task := pmAssetDraft(t)
			ctx := context.Background()
			yes, no := true, false
			u := core.Unreachable{ID: "frames", Source: "landing", Criterion: "Frames", Why: "generator unavailable", Finding: "original", AssetCreation: &yes, Revision: 1}
			var err error
			task, err = w.a.Core.RetainLandingAssets(ctx, task, []core.Unreachable{u}, "PM", false, p.Brief.Version)
			if err != nil {
				t.Fatal(err)
			}
			u.AssetCreation = &no
			task, err = w.a.Core.RetainLandingAssets(ctx, task, []core.Unreachable{u}, "PM", false, p.Brief.Version)
			if err != nil {
				t.Fatal(err)
			}
			if rejectDecision {
				db, err := sql.Open("sqlite", filepath.Join(w.a.Core.StateDirectory(), "state.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err = db.Exec(`CREATE TRIGGER reject_pm_decision BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'synthetic decision failure'); END;`); err != nil {
					t.Fatal(err)
				}
				if _, err = w.a.Core.DecideLanding(ctx, task.ID, core.LandDecision{Revision: 1, Land: true, Reason: "Ready"}, core.DecisionInput{}); err == nil {
					t.Fatal("rejected decision committed")
				}
				if _, err = db.Exec(`DROP TRIGGER reject_pm_decision`); err != nil {
					t.Fatal(err)
				}
			}
			w.a = restart(t, w.a)
			got := stepUntil(t, w.a, task.ID, func(task core.Task) bool { return task.LandDecision != nil || task.DecisionID != "" })
			if got.Approved != 1 || got.DecisionID != "" || len(got.Pending(1)) != 0 || len(got.Unreachable) != 1 || got.Unreachable[0].Finding != "original" || got.Unreachable[0].AssetCreation == nil || *got.Unreachable[0].AssetCreation {
				t.Fatal("false PM classification escalated or lost its history", got)
			}
		})
	}
}

func TestPMLandingRetainsDistinctObstaclesThroughCorrections(t *testing.T) {
	for name, second := range map[string]string{
		"Icons mismatched ID":     `{"land":true,"reason":"ready","blocked_asset":{"report_id":"FRAMES_ID","requirement":"Icons","why":"generator unavailable","asset_creation":false}}`,
		"unknown initial ID":      `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Icons","why":"code only","asset_creation":false}}`,
		"Icons unknown ID":        `{"land":true,"reason":"ready","blocked_asset":{"report_id":"unknown","requirement":"Icons","why":"generator unavailable","asset_creation":true}}`,
		"code-only":               `{"land":true,"reason":"ready","asset_creation":false}`,
		"Icons false":             `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Icons","why":"code only","asset_creation":false}}`,
		"Icons true":              `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Icons","why":"generator unavailable","asset_creation":true}}`,
		"Frames false":            `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Frames","why":"code only","asset_creation":false}}`,
		"omitted":                 `{"land":true,"reason":"ready"}`,
		"null":                    `{"land":true,"reason":"ready","blocked_asset":null}`,
		"invalid":                 `{"land":true,"reason":"ready","blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":"false"}}`,
		"failed correction":       `{"land":"invalid","reason":45}`,
		"runner failure":          "",
		"code-only then omission": `{"land":true,"reason":"ready"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
			ctx := context.Background()
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Assets", Criteria: []string{"Frames", "Icons"}})
			if err != nil {
				t.Fatal(err)
			}
			seatDesigner(t, w.a, w.p.ID)
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			snap, _ := w.a.Core.Snapshot(ctx)
			p, _ := findProject(snap, w.p.ID)
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Status = core.TaskDeciding
				for _, checker := range task.Checkers() {
					task.Verdicts = append(task.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, TextVersion: task.TextVersion, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			first := `{"land":"invalid","reason":45,"blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":true}}`
			if name == "unknown initial ID" {
				first = strings.Replace(first, `"requirement":"Frames"`, `"report_id":"unknown","requirement":"Frames"`, 1)
			}
			if name == "code-only then omission" {
				first = strings.Replace(first, `"asset_creation":true`, `"asset_creation":false`, 1)
			}
			if name == "Icons mismatched ID" {
				u, err := parseLandingAsset(p, task, first)
				if err != nil {
					t.Fatal(err)
				}
				second = strings.Replace(second, "FRAMES_ID", u.ID, 1)
			}
			w.runner.pmLand = []string{first, second}
			if name == "runner failure" {
				w.runner.fail = []error{nil, errors.New("synthetic PM failure")}
			}
			m, _ := w.a.mediumFor(ctx, p, task.Playbook)
			if err = w.a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
				t.Fatal(err)
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if name == "code-only then omission" {
				if got.Approved != 1 || got.Status != core.TaskLanding || len(got.Unreachable) != 0 || got.DecisionID != "" {
					t.Fatal("code-only correction could not land", got)
				}
				return
			}
			if name != "runner failure" {
				w.runner.mu.Lock()
				guided := false
				for _, spec := range w.runner.seen {
					guided = guided || strings.Contains(spec.Prompt, "pending report_id "+got.Unreachable[0].ID)
				}
				w.runner.mu.Unlock()
				if !guided {
					t.Fatal("correction prompt omitted the durable obstacle identity")
				}
			}
			if name == "Frames false" {
				if pendingLandingAssets(got) || got.Unreachable[0].AssetCreation == nil || *got.Unreachable[0].AssetCreation {
					t.Fatal("matching correction lost", got)
				}
				return
			}
			if got.Status != core.TaskWriting || got.DecisionID != "" || got.LandDecision != nil || len(got.Unreachable) == 0 {
				t.Fatal(got)
			}
			frames := got.Unreachable[0]
			classified := frames.AssetCreation != nil && *frames.AssetCreation
			if frames.Criterion != "Frames" || classified != (name != "unknown initial ID") || frames.ID == "" || frames.Finding != "sandbox" {
				t.Fatal(frames)
			}
			if (name == "Icons true" || name == "Icons unknown ID" || name == "Icons mismatched ID") && (len(got.Unreachable) != 2 || got.Unreachable[1].ID == frames.ID) {
				t.Fatal("distinct reports lost", got.Unreachable)
			}
			w.a = restart(t, w.a)
			snap, _ = w.a.Core.Snapshot(ctx)
			got, _ = snap.FindTask(task.ID)
			if got.Unreachable[0].ID != frames.ID || !pendingLandingAssets(got) {
				t.Fatal("restart lost reports", got)
			}
			if len(got.Unreachable) > 1 {
				err = writerAssetError(got, writerReply{assets: []core.WantedAsset{{Name: "frame", Want: "PNG"}}, requirements: []string{"Frames"}})
				if err != nil {
					t.Fatal(fmt.Sprintf("named coverage rejected: %v", err))
				}
			}
		})
	}
}

func TestPMLandingAcceptsEmptyCodeOnlyClassifications(t *testing.T) {
	for _, extra := range []string{`"blocked_asset":null`, `"asset_creation":false`} {
		for _, land := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/land=%v", extra, land), func(t *testing.T) {
				w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
				ctx := context.Background()
				task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Code only"})
				if err != nil {
					t.Fatal(err)
				}
				seatDesigner(t, w.a, w.p.ID)
				w.runner.pmLand = []string{fmt.Sprintf(`{"land":%v,"reason":"Ready",%s}`, land, extra)}
				task = stepUntil(t, w.a, task.ID, func(task core.Task) bool { return task.LandDecision != nil })
				if task.LandDecision.Land != land || task.Failures != 0 || len(task.Unreachable) != 0 {
					t.Fatal("ordinary classification rejected", task)
				}
			})
		}
	}
}
