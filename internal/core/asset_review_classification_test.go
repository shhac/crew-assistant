package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReviewClassificationActivityRequiresStoredReports(t *testing.T) {
	for _, mode := range []string{"empty", "obsolete", "false", "true", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s, current := routedDraftFixture(t)
			snap, _ := s.Snapshot(testContext)
			briefVersion := project(&snap, current.ProjectID).Brief.Version
			yes, no := true, false
			var reports []Unreachable
			if mode == "obsolete" || mode == "mixed" {
				reports = append(reports, Unreachable{ID: "retired", Source: "review", Bound: "task", Criterion: "Removed requirement", AssetCreation: &yes})
			}
			if mode == "false" || mode == "true" || mode == "mixed" {
				classification := &no
				if mode == "true" {
					classification = &yes
				}
				reports = append(reports, Unreachable{ID: "current", Source: "review", Criterion: "Frames", AssetCreation: classification})
			}
			activityCounts := func(snap Snapshot) (stored, retired int) {
				for _, event := range snap.Activity {
					if event.Kind == "task.review_assets" {
						stored++
					}
					if event.Kind == "task.asset_obsolete" {
						retired++
					}
				}
				return
			}
			beforeStored, beforeRetired := activityCounts(snap)
			got, err := s.RouteReviewAssets(testContext, current, reports, "PM", briefVersion)
			if err != nil {
				t.Fatal(err)
			}
			s = reopenAssetService(t, s)
			snap, _ = s.Snapshot(testContext)
			stored, retired := activityCounts(snap)
			stored, retired = stored-beforeStored, retired-beforeRetired
			wantStored, wantRetired := 0, 0
			if mode == "false" || mode == "true" || mode == "mixed" {
				wantStored = 1
			}
			if mode == "obsolete" || mode == "mixed" {
				wantRetired = 1
			}
			if stored != wantStored || retired != wantRetired || (mode != "true" && got.Status != TaskDeciding) {
				t.Fatalf("%s: classifications=%d retirements=%d status=%s", mode, stored, retired, got.Status)
			}
		})
	}
}

func TestReviewClassificationFencesAndRetirement(t *testing.T) {
	s, current := routedDraftFixture(t)
	yes, no := true, false
	reports := []Unreachable{{ID: "frames", Source: "review", Criterion: "Frames", Finding: "generate", Why: "generate", Revision: 1, AssetCreation: &yes}, {ID: "icons", Source: "review", Criterion: "Icons", Finding: "generate", Why: "generate", Revision: 1, AssetCreation: &no}}
	snap, _ := s.Snapshot(testContext)
	p := project(&snap, current.ProjectID)
	if _, err := s.RouteReviewAssets(testContext, current, reports, "PM", p.Brief.Version+1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	stale := current
	stale.TextVersion++
	if _, err := s.RouteReviewAssets(testContext, stale, reports, "PM", p.Brief.Version); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	got, err := s.RouteReviewAssets(testContext, current, reports, "PM", p.Brief.Version)
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ = s.Snapshot(testContext)
	got, _ = snap.FindTask(current.ID)
	if got.Status != TaskWriting || reportFor(got.Unreachable, "icons") == nil || *reportFor(got.Unreachable, "icons").AssetCreation {
		t.Fatal(got)
	}
	got, err = s.UpdateTask(testContext, got.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskDeciding; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	u := *reportFor(got.Unreachable, "frames")
	u.AssetCreation = nil
	got, err = s.RouteAsset(testContext, got, u, "PM", p.Brief.Version)
	if err != nil || !*reportFor(got.Unreachable, "frames").AssetCreation {
		t.Fatal(got, err)
	}
	got, err = s.UpdateTask(testContext, got.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskDeciding
		t.Criteria = []string{"Timing"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reports[0].Bound = "task"
	got, err = s.RouteReviewAssets(testContext, got, reports[:1], "PM", p.Brief.Version)
	if err != nil || reportFor(got.Unreachable, "frames") != nil || got.Status != TaskDeciding {
		t.Fatal(got, err)
	}
	snap, _ = s.Snapshot(testContext)
	found := false
	for _, e := range snap.Activity {
		if e.Kind == "task.asset_obsolete" {
			found = true
		}
	}
	if !found {
		t.Fatal("retired review report missing from activity")
	}
}

func TestOwnerAdoptionSettlesOnlyVerifiedRequestsAtomicallyAcrossRestart(t *testing.T) {
	s, current := routedDraftFixture(t)
	current, err := s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
		t.Design = []DesignRequest{{ID: "frames", AnsweredAt: s.now(), IntegrationSuspended: true, Production: &Production{Delivered: []DeliveredAsset{{}}}}, {ID: "icons", AnsweredAt: s.now(), IntegrationPending: true, Production: &Production{Delivered: []DeliveredAsset{{}}}}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := OwnerIntegration{Expected: current, Requests: []string{"frames"}}
	got, err := s.AdoptDraft(testContext, current.ID, Revision{Ref: "owner"}, false, evidence)
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	got, _ = snap.FindTask(current.ID)
	if got.Design[0].IntegrationSuspended || got.Design[0].IntegrationPending || !got.Design[1].IntegrationPending || len(got.Revisions) != 2 {
		t.Fatal(got)
	}
	got, err = s.AdoptDraft(testContext, current.ID, Revision{Ref: "stale"}, false, evidence)
	if err != nil || len(got.Revisions) != 3 || !got.Design[1].IntegrationPending {
		t.Fatal(got, err)
	}
}

func TestOwnerAdoptionKeepsIntegrationPendingWhenCoverageBecomesStale(t *testing.T) {
	s, current := routedDraftFixture(t)
	current, err := s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
		t.Design = []DesignRequest{{ID: "frames", AnsweredAt: s.now(), IntegrationPending: true, Production: &Production{Delivered: []DeliveredAsset{{}}}}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := OwnerIntegration{Expected: current, Requests: []string{"frames"}}
	_, err = s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
		t.TextVersion++
		t.Criteria = append(t.Criteria, "New requirement")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AdoptDraft(testContext, current.ID, Revision{Ref: "owner"}, true, evidence)
	if err != nil || len(got.Revisions) != 2 || !got.NeedsAssetIntegration() || got.Approved != 2 || got.Status != TaskReviewing {
		t.Fatal(got, err)
	}
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	got, _ = snap.FindTask(current.ID)
	if len(got.Revisions) != 2 || !got.NeedsAssetIntegration() {
		t.Fatal(got)
	}
}

func TestLinkedAssetClassificationCannotBeSilentlyDiscarded(t *testing.T) {
	for _, id := range []string{"", "frames"} {
		t.Run(id, func(t *testing.T) {
			s, current := routedDraftFixture(t)
			yes, no := true, false
			old := Unreachable{ID: id, Criterion: "Frames", Why: "generator", Revision: 1, AssetCreation: &yes}
			current, err := s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
				t.Status = TaskWriting
				t.Design = []DesignRequest{{AnsweredAt: s.now(), Production: &Production{}, AssetReports: []Unreachable{old}}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			correction := old
			correction.AssetCreation = &no
			_, err = s.RecordAssetClassification(testContext, current, []Unreachable{correction}, "Writer", nil)
			if err == nil || !strings.Contains(err.Error(), "already covered by production") {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			got, _ := snap.FindTask(current.ID)
			if got.Status != current.Status || !reflect.DeepEqual(got.Unreachable, current.Unreachable) || len(got.Revisions) != len(current.Revisions) {
				t.Fatal(got)
			}
		})
	}
}
