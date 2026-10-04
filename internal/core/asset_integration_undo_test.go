package core

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestCompletedProductionSuspendsAndReactivates(t *testing.T) {
	for _, source := range []string{"task", "brief", "owner checks"} {
		for _, integrated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/integrated=%v", source, integrated), func(t *testing.T) {
				s, current := routedDraftFixture(t)
				current, err := s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
					task.Criteria = []string{"Frames"}
					task.Unreachable = nil
					task.Design = []DesignRequest{{ID: "frames", AnsweredAt: s.now(), IntegrationPending: !integrated, AssetReports: []Unreachable{{ID: "frames-report", Criterion: "Frames", Bound: "task"}}, Production: &Production{Delivered: []DeliveredAsset{{Attachment: "asset"}}, Provenance: "provenance", Archive: "archive"}}}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				evidence := current.Design[0]
				if source == "brief" {
					if _, err = s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
						t.Fatal(err)
					}
				}
				after := TaskText{Objective: current.Objective}
				if source == "owner checks" {
					after.OwnerChecks = []string{"Frames"}
				}
				err = s.store.update(testContext, func(v *Snapshot) error {
					var out Task
					return s.applyEdit(v, task(v, current.ID), TaskEdit{By: FromOwner, Kind: FromOwner, After: after}, &out)
				})
				if err != nil {
					t.Fatal(err)
				}
				if source == "brief" {
					snap, _ := s.Snapshot(testContext)
					got, _ := snap.FindTask(current.ID)
					if got.NeedsAssetIntegration() == integrated {
						t.Fatal("duplicate brief authority lost")
					}
					if _, err = s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets"}); err != nil {
						t.Fatal(err)
					}
				}
				for cycle := 0; cycle < 2; cycle++ {
					s = reopenAssetService(t, s)
					snap, _ := s.Snapshot(testContext)
					got, _ := snap.FindTask(current.ID)
					if got.NeedsAssetIntegration() || got.Design[0].IntegrationSuspended == integrated {
						t.Fatal("lost suspension", got.Design)
					}
					if source == "brief" {
						if _, err = s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err = s.UndoTaskEdit(testContext, current.ProjectID, current.ID, got.Edits[len(got.Edits)-1].ID); err != nil {
							t.Fatal(err)
						}
					}
					s = reopenAssetService(t, s)
					snap, _ = s.Snapshot(testContext)
					got, _ = snap.FindTask(current.ID)
					if got.NeedsAssetIntegration() == integrated || got.Design[0].IntegrationSuspended {
						t.Fatal("lost reactivation", got.Design)
					}
					restored := got.Design[0]
					restored.IntegrationPending = evidence.IntegrationPending
					if !reflect.DeepEqual(restored, evidence) {
						t.Fatal("lost production evidence", restored)
					}
					if source == "brief" {
						if _, err = s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets"}); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err = s.UndoTaskEdit(testContext, current.ProjectID, current.ID, got.Edits[len(got.Edits)-1].ID); err != nil {
							t.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestIntegrationReactivationIsAtomicWithBriefUpdate(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s, current := routedDraftFixture(t)
			_, err := s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
				task.Criteria = nil
				task.Unreachable = nil
				task.Design = []DesignRequest{{ID: "frames", AnsweredAt: s.now(), IntegrationSuspended: true, AssetReports: []Unreachable{{ID: "report", Criterion: "Frames", Bound: "brief"}}, Production: &Production{Delivered: []DeliveredAsset{{Attachment: "asset"}}, Provenance: "provenance"}}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.Snapshot(testContext)
			ctx := testContext
			if cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else {
				if _, err = s.store.db.Exec(`CREATE TRIGGER reject_asset_reactivation BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END;`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.UpdateBrief(ctx, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err == nil {
				t.Fatal("rejected reactivation succeeded")
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("brief and integration did not roll back together")
			}
		})
	}
}
