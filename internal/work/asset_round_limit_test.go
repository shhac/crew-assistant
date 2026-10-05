package work

import (
	"context"
	"fmt"
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
)

func TestRoundLimitPersistsEveryClassificationAcrossCorrectionAndRestart(t *testing.T) {
	t.Parallel()
	for _, reboot := range []bool{false, true} {
		t.Run(fmt.Sprint(reboot), func(t *testing.T) {
			t.Parallel()
			runner := &scriptedRunner{escalate: []string{`{"findings":[{"finding":1,"asset_creation":true},{"finding":2,"asset_creation":true}],"wrong_way":false,"reason":"assets"}`, "invalid", "invalid"}}
			a, p, task := draftTeam(t, runner, true, "1")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Frames", Note: "Generate frames"}, {Criterion: "Icons", Note: "Generate icons"}}}}
			fs := remaining(changes)
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				no := false
				t.Unreachable = []core.Unreachable{reviewReport(p, *t, fs[0], &no), reviewReport(p, *t, fs[1], &no)}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = a.escalate(ctx, p, task, nil, changes); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Status != core.TaskWriting || len(task.Unreachable) != 2 {
				t.Fatal(task)
			}
			for _, u := range task.Unreachable {
				if u.AssetCreation == nil || !*u.AssetCreation || !u.NeedsAssetReply() {
					t.Fatal(u)
				}
			}
			no := false
			correction := task.Unreachable[0]
			correction.AssetCreation = &no
			task, err = a.Core.RecordAssetClassification(ctx, task, []core.Unreachable{correction}, "Writer", nil, p.Brief.Version)
			if err != nil {
				t.Fatal(err)
			}
			if reboot {
				a = restart(t, a)
				a.runner = runner
			}
			if err = a.escalate(ctx, p, task, nil, changes); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Status != core.TaskWriting || task.DecisionID != "" || *task.Unreachable[0].AssetCreation || !*task.Unreachable[1].AssetCreation || !task.Unreachable[1].NeedsAssetReply() {
				t.Fatal(task)
			}
		})
	}
}

func TestRoundLimitStoresFalseBeforeOpeningOwnerDecision(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{escalate: []string{`{"findings":[{"finding":1,"asset_creation":false},{"finding":2,"asset_creation":false}],"wrong_way":false,"reason":"code"}`}}
	a, p, task := draftTeam(t, runner, true, "1")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskDeciding; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Frames", Note: "timing"}, {Criterion: "Icons", Note: "decode"}}}}
	if err = a.escalate(ctx, p, task, nil, changes); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.DecisionID == "" || len(got.Unreachable) != 2 {
		t.Fatal(got)
	}
	for _, u := range got.Unreachable {
		if u.AssetCreation == nil || *u.AssetCreation || u.Routed != "" {
			t.Fatal(u)
		}
	}
}

func TestPartialObsoleteReviewJudgementNeverEscalates(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{escalate: []string{`{"findings":[{"finding":1,"asset_creation":true}]}`, "invalid"}}
	a, p, task := draftTeam(t, runner, true, "1")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Frames", Note: "generate"}}}}
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDeciding
		yes := true
		u := reviewReport(p, *t, remaining(changes)[0], &yes)
		u.Bound = "task"
		t.Unreachable = []core.Unreachable{u}
		t.Criteria = []string{"Timing"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.escalate(ctx, p, task, nil, changes); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.DecisionID != "" || got.Status != core.TaskDeciding || len(got.Unreachable) != 0 {
		t.Fatal(got)
	}
}
