//go:build !windows

package work

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

type failedAssetPublication struct{ medium }

func (m failedAssetPublication) publish(context.Context, core.Task, string, string) error {
	return errors.New("synthetic publication failure")
}

func TestAssetHandoffRecoveryRetainsAndSettlesRecordedCoverage(t *testing.T) {
	for _, stage := range []string{"before prepare", "prepared", "published", "committed", "failed publication"} {
		for _, covered := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "/suspended turn", true: "/integration turn"}[covered], func(t *testing.T) {
				a, _, task, m, h := handoffAt(t)
				ctx := context.Background()
				var err error
				task, err = a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
					task.Design = []core.DesignRequest{
						{ID: "frames", AnsweredAt: time.Now(), IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
						{ID: "late-icons", AnsweredAt: time.Now(), IntegrationPending: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
						{ID: "suspended", AnsweredAt: time.Now(), IntegrationSuspended: true, Production: &core.Production{Delivered: []core.DeliveredAsset{{}}}},
					}
					if covered {
						h.IntegratedDesign = []string{"frames"}
					}
					task.Handoff = &h
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				recorded := stage != "before prepare" && stage != "failed publication"
				switch stage {
				case "before prepare":
					if err = a.dropHandoff(ctx, task.ID, h.Name); err != nil {
						t.Fatal(err)
					}
				case "published", "committed":
					if err = m.publish(ctx, task, h.Revision.Ref, h.Name); err != nil {
						t.Fatal(err)
					}
					if stage == "committed" {
						if err = a.commitHandoff(ctx, task.ID, h.Name); err != nil {
							t.Fatal(err)
						}
					}
				case "failed publication":
					if err = a.dropHandoff(ctx, task.ID, h.Name); err != nil {
						t.Fatal(err)
					}
					if err = a.handOff(ctx, task, failedAssetPublication{m}, h); err != nil {
						t.Fatal(err)
					}
				}
				for replay := 0; replay < 2; replay++ {
					a = restart(t, a)
					snap, _ := a.Core.Snapshot(ctx)
					got, _ := snap.FindTask(task.ID)
					if got.Handoff != nil || len(got.Revisions) != map[bool]int{false: 0, true: 1}[recorded] {
						t.Fatal(got)
					}
					if got.Design[0].IntegrationPending == (covered && recorded) || !got.Design[1].IntegrationPending || !got.Design[2].IntegrationSuspended {
						t.Fatal("recovery lost coverage", got.Design)
					}
				}
			})
		}
	}
}
