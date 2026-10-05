package core

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestLandingAssetEvidenceSurvivesRestartAndRoutesTogether(t *testing.T) {
	s, current := routedDraftFixture(t)
	yes, no := true, false
	current.Unreachable = nil
	var err error
	current, err = s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
		task.Unreachable = nil
		task.Criteria = []string{"Frames", "Icons"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	frames := Unreachable{ID: "frames", Source: "landing", Criterion: "Frames", Why: "generator", Finding: "original", Bound: "task", AssetCreation: &yes}
	current, err = s.RetainLandingAssets(testContext, current, []Unreachable{frames}, "PM", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	current, _ = snap.FindTask(current.ID)
	current, err = s.RetainLandingAssets(testContext, current, []Unreachable{{ID: "icons", Source: "landing", Criterion: "Icons", Why: "code", AssetCreation: &no}}, "PM", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Unreachable) != 1 || !reflect.DeepEqual(current.Unreachable[0], frames) {
		t.Fatal(current.Unreachable)
	}
	current, err = s.RetainLandingAssets(testContext, current, []Unreachable{{ID: "icons", Source: "landing", Criterion: "Icons", Why: "generator", AssetCreation: &yes}}, "PM", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	current, err = s.RetainLandingAssets(testContext, current, nil, "PM", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != TaskWriting || len(current.Unreachable) != 2 || current.Unreachable[0].Routed == "" || current.Unreachable[1].Routed == "" {
		t.Fatal(current)
	}
}

func TestSchedulerRecoversRetainedLandingAssets(t *testing.T) {
	s, current := routedDraftFixture(t)
	current, err := s.UpdateTask(testContext, current.ID, func(task *Task, p *Project) (string, error) {
		task.Unreachable = nil
		p.Settings.Land = LandPolicy{Via: LandPush, Target: "main", Approve: ApprovePM}
		p.Settings.Roles = append(p.Settings.Roles, Role{Name: "PM", Kinds: []string{RolePM}, Engine: "claude"})
		task.Playbook.Land = p.Settings.Land
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	current, err = s.RetainLandingAssets(testContext, current, []Unreachable{{ID: "frames", Source: "landing", Criterion: "Frames", Why: "generator", AssetCreation: &yes}}, "PM", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	out, err := s.Schedule(testContext, anyone)
	if err != nil || len(out) != 1 || out[0].Task.ID != current.ID || out[0].Claim.Step != TaskWriting || out[0].Task.Unreachable[0].Routed == "" {
		t.Fatal("retained evidence stranded at scheduler gate", out, err)
	}
}

func TestSchedulerRecoversIntegrationAfterConcurrentReview(t *testing.T) {
	for _, status := range []string{TaskDeciding, TaskLanding, TaskAwaiting} {
		t.Run(status, func(t *testing.T) {
			s, current := routedDraftFixture(t)
			_, err := s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
				task.Status, task.Unreachable = status, nil
				task.Proposal = &Proposal{Number: 12}
				task.Playbook.Land.PullRequests = true
				task.Design = []DesignRequest{{ID: "frames", AnsweredAt: s.now(), IntegrationPending: true, AssetReports: []Unreachable{{ID: "frames", Criterion: "Frames", Bound: "task"}}, Production: &Production{Delivered: []DeliveredAsset{{Attachment: "asset"}}, Provenance: "provenance"}}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s = reopenAssetService(t, s)
			out, err := s.Schedule(testContext, anyone)
			if err != nil || len(out) != 1 || out[0].Claim.Step != TaskWriting || !out[0].Task.NeedsAssetIntegration() {
				t.Fatal("integration stranded after review or PR wait", out, err)
			}
		})
	}
}

func TestLandingAssetWritesRejectStaleCancelledAndFailedRecords(t *testing.T) {
	for _, mode := range []string{"store", "cancelled", "claim", "edit", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			s, current := routedDraftFixture(t)
			before, _ := s.Snapshot(testContext)
			yes := true
			reports := []Unreachable{{ID: "landing", Source: "landing", Criterion: "Frames", Why: "generator", AssetCreation: &yes}}
			ctx := testContext
			switch mode {
			case "store":
				if _, err := s.store.db.Exec(`CREATE TRIGGER reject_landing_assets BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END;`); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "claim":
				ctx = Fenced(ctx, current.ID, "revoked")
			case "edit":
				_, err := s.UpdateTask(ctx, current.ID, func(task *Task, _ *Project) (string, error) { task.TextVersion++; return "", nil })
				if err != nil {
					t.Fatal(err)
				}
				before, _ = s.Snapshot(ctx)
			case "concurrent":
				var wg sync.WaitGroup
				results := make(chan error, 2)
				for i := 0; i < 2; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, err := s.RetainLandingAssets(ctx, current, reports, "PM", true, 0)
						results <- err
					}()
				}
				wg.Wait()
				close(results)
				successes := 0
				for err := range results {
					if err == nil {
						successes++
					} else if !errors.Is(err, ErrConflict) {
						t.Fatal(err)
					}
				}
				if successes != 1 {
					t.Fatal(successes)
				}
				return
			}
			if _, err := s.RetainLandingAssets(ctx, current, reports, "PM", true, 0); err == nil {
				t.Fatal("rejected write succeeded")
			}
			after, _ := s.Snapshot(testContext)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("partial record commit")
			}
		})
	}
}
