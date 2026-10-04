package work

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestAssetHandoffSettlesOnlyTurnCoverage(t *testing.T) {
	for _, covered := range []bool{false, true} {
		t.Run(map[bool]string{false: "suspended turn", true: "integration turn"}[covered], func(t *testing.T) {
			task := core.Task{Design: []core.DesignRequest{
				{ID: "frames", AnsweredAt: time.Now(), IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
				{ID: "icons", AnsweredAt: time.Now(), IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
				{ID: "suspended", AnsweredAt: time.Now(), IntegrationSuspended: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
			}}
			h := core.Handoff{Revision: core.Revision{N: 1, Ref: "new-draft"}}
			if covered {
				h.IntegratedDesign = []string{"frames"}
			}
			applyHandoff(&task, &core.Project{}, h)
			if task.Design[0].IntegrationPending == covered || !task.Design[1].IntegrationPending || !task.Design[2].IntegrationSuspended {
				t.Fatal("handoff cleared unrelated integration", task.Design)
			}
		})
	}
}

func TestAssetIntegrationReactivationFencesApprovalAndDelivery(t *testing.T) {
	a, _, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	var err error
	task, err = a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Status = core.TaskLanding
		task.Revisions = []core.Revision{{N: 1, Ref: "old-draft"}}
		task.Design = []core.DesignRequest{{ID: "frames", IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.approve(ctx, task); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Approved != 0 || got.Status != core.TaskWriting {
		t.Fatal("approval bypassed integration", got)
	}
	held, err := a.beginDelivering(ctx, task.ID, task.Revisions[0])
	if err != nil || len(held) == 0 {
		t.Fatal("delivery bypassed integration", held, err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	got, _ = snap.FindTask(task.ID)
	if got.Delivering != nil {
		t.Fatal("delivery intent recorded despite integration")
	}
}

func TestUnchangedPRTurnCannotBypassConcurrentReactivation(t *testing.T) {
	task := core.Task{
		Revisions: []core.Revision{{N: 1, Ref: "old-draft"}},
		Design:    []core.DesignRequest{{ID: "frames", IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}}},
	}
	noChangeNeeded(&task, core.Handoff{Reply: "Nothing to change"})
	if task.Status != core.TaskWriting || !task.NeedsAssetIntegration() || len(task.Revisions) != 1 {
		t.Fatal("unchanged reply bypassed the current integration obligation", task)
	}
}

func TestPreparedDraftKeepsLaterLandingEvidence(t *testing.T) {
	for _, classification := range []*bool{nil, new(bool)} {
		u := core.Unreachable{ID: "frames", Source: "landing", Criterion: "Frames", Finding: "generator unavailable", Why: "generator unavailable", Revision: 1, AssetCreation: classification}
		task := core.Task{Criteria: []string{"Frames"}, Revisions: []core.Revision{{N: 1}}, Unreachable: []core.Unreachable{u}}
		applyHandoff(&task, &core.Project{}, core.Handoff{Revision: core.Revision{N: 2}})
		if len(task.Unreachable) != 1 || task.Unreachable[0].ID != u.ID || task.Unreachable[0].Finding != u.Finding || task.Unreachable[0].Revision != 2 {
			t.Fatal("prepared draft discarded later PM evidence", task.Unreachable)
		}
		if classification != nil && len(task.Pending(2)) != 0 {
			t.Fatal("settled PM classification became implementer unmet work")
		}
	}
}
