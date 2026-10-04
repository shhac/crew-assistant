package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestProductionRecoveryRespectsOwnerTransferBeforeFirstDraft(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			s, current := routedDraftFixture(t)
			if _, err := s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
				t.Fatal(err)
			}
			current, err := s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
				t.Status = TaskWriting
				t.Revisions = nil
				t.Criteria = nil
				t.Unreachable = t.Unreachable[:1]
				t.Unreachable[0].Bound = "brief"
				t.Unreachable[0].Revision = 0
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			current, err = s.AskDesign(testContext, current.ID, DesignAsk{Expected: &current, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				err = s.store.update(testContext, func(v *Snapshot) error {
					t := task(v, current.ID)
					var out Task
					return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: t.Objective, OwnerChecks: []string{"Frames"}}}, &out)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			current, err = s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
				t.Roles = slices.DeleteFunc(t.Roles, func(r Role) bool { return r.Holds(RoleDesigner) })
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s = reopenAssetService(t, s)
			snap, _ := s.Snapshot(testContext)
			current, _ = snap.FindTask(current.ID)
			current, err = s.RestoreUnfinishedProduction(testContext, current)
			if err != nil {
				t.Fatal(err)
			}
			current, err = s.PrepareAssetWriting(testContext, current)
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				if len(current.Unreachable) != 0 || !slices.Contains(current.OwnerChecks, "Frames") {
					t.Fatal(current)
				}
			} else if len(current.Unreachable) != 1 || current.Unreachable[0].AssetCreation == nil || !*current.Unreachable[0].AssetCreation || !strings.Contains(current.Unreachable[0].Why, "no designer") {
				t.Fatal(current)
			}
		})
	}
}

func TestAssetOriginalBriefReportsRetireBeforeJudgementAcrossRestart(t *testing.T) {
	for _, keepInTask := range []bool{false, true} {
		t.Run(fmt.Sprint("task retains criterion=", keepInTask), func(t *testing.T) {
			s, task := routedDraftFixture(t)
			p, err := s.UpdateBrief(testContext, task.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Brief frames"}})
			if err != nil {
				t.Fatal(err)
			}
			task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				// Exactly the report format from before asset routing existed.
				t.Unreachable = []Unreachable{{Criterion: "Brief frames", Why: "sandbox cannot generate frames", Revision: 1}, {Criterion: "Timing", Why: "code", Revision: 1}}
				if keepInTask {
					t.Criteria = append(t.Criteria, "Brief frames")
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			original := task.Unreachable[0]
			p, err = s.UpdateBrief(testContext, p.ID, BriefInput{Goal: "Assets", Criteria: []string{"Replacement"}})
			if err != nil {
				t.Fatal(err)
			}
			s = reopenAssetService(t, s)
			snap, _ := s.Snapshot(testContext)
			task, _ = snap.FindTask(task.ID)
			got, err := s.RouteAsset(testContext, task, original, "classification", p.Brief.Version)
			if keepInTask {
				if err != nil || got.Status != TaskWriting || len(got.Unreachable) != 2 {
					t.Fatal(got, err)
				}
			} else {
				if !errors.Is(err, ErrConflict) || len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "Timing" || len(task.Design) != 0 {
					t.Fatal(task, err)
				}
			}
		})
	}
}

func TestAssetCurrentRequirementsSurviveRemovalAndUndoAcrossRestart(t *testing.T) {
	for _, fromBrief := range []bool{false, true} {
		t.Run(fmt.Sprint("brief retains criterion=", fromBrief), func(t *testing.T) {
			s, task := routedDraftFixture(t)
			if fromBrief {
				if _, err := s.UpdateBrief(testContext, task.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
					t.Fatal(err)
				}
			}
			task, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
			if err != nil {
				t.Fatal(err)
			}
			err = s.store.update(testContext, func(v *Snapshot) error {
				var out Task
				return s.applyEdit(v, taskRecordForTest(v, task.ID), TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: task.Objective, Criteria: []string{"Timing"}}}, &out)
			})
			if err != nil {
				t.Fatal(err)
			}
			snap, _ := s.Snapshot(testContext)
			task, _ = snap.FindTask(task.ID)
			if !fromBrief {
				task, err = s.UndoTaskEdit(testContext, task.ProjectID, task.ID, task.Edits[len(task.Edits)-1].ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			s = reopenAssetService(t, s)
			got, err := s.PrepareAssetWriting(testContext, task)
			if err != nil || len(got.Unreachable) != 2 || (reportFor(got.Unreachable, "Frames") == nil || reportFor(got.Unreachable, "Frames").Routed == "") || len(got.Revisions) != 1 {
				t.Fatal(got, err)
			}
			// The preserved route must still be usable for a linked spec.
			_, err = s.AskDesign(testContext, got.ID, DesignAsk{Expected: &got, From: "Writer", Question: "Frames", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAssetRouteReconcilesRemovalBeforeRedirectAcrossRestart(t *testing.T) {
	s, task := routedDraftFixture(t)
	origin := task.Unreachable[0]
	err := s.store.update(testContext, func(v *Snapshot) error {
		var out Task
		return s.applyEdit(v, taskRecordForTest(v, task.ID), TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: task.Objective, Criteria: []string{"Timing"}}}, &out)
	})
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	got, err := s.RouteAsset(testContext, task, origin, "Writer")
	if !errors.Is(err, ErrConflict) {
		t.Fatal("late removed report accepted", got, err)
	}
	// A pending record from before edit reconciliation must also be retired
	// before it can be restamped as a current production obligation.
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Unreachable = append(t.Unreachable, origin)
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	got, err = s.RouteAsset(testContext, task, origin, "Writer")
	if err != nil || got.Status != TaskDeciding || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Timing" || len(got.Design) != 0 {
		t.Fatal(got, err)
	}
}

func TestAssetBriefEditsFenceJudgementClassificationAndProduction(t *testing.T) {
	for _, phase := range []string{"judgement", "classification", "production"} {
		t.Run(phase, func(t *testing.T) {
			s, task := routedDraftFixture(t)
			snap, _ := s.Snapshot(testContext)
			p := *project(&snap, task.ProjectID)
			p, err := s.UpdateBrief(testContext, p.ID, BriefInput{Goal: "Assets", Criteria: []string{"Brief frames"}})
			if err != nil {
				t.Fatal(err)
			}
			task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				t.Unreachable[0].Criterion, t.Unreachable[0].Bound, t.Unreachable[0].BriefVersion = "Brief frames", "brief", p.Brief.Version
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if phase != "judgement" {
				task, err = s.RouteAsset(testContext, task, task.Unreachable[0], "Writer", p.Brief.Version)
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err = s.UpdateBrief(testContext, p.ID, BriefInput{Goal: "Assets", Criteria: []string{"Replacement"}})
			if err != nil {
				t.Fatal(err)
			}
			afterBrief, _ := s.Snapshot(testContext)
			beforeStale, _ := afterBrief.FindTask(task.ID)
			switch phase {
			case "judgement":
				_, err = s.RouteAsset(testContext, task, task.Unreachable[0], "PM", p.Brief.Version)
			case "classification":
				_, err = s.RecordAssetClassification(testContext, task, task.Unreachable[:1], "Writer", nil, p.Brief.Version)
			case "production":
				_, err = s.AskDesign(testContext, task.ID, DesignAsk{Expected: &task, BriefVersion: p.Brief.Version, From: "Writer", Question: "Frames", Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Requirements: []string{"Brief frames"}, Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			snap, _ = s.Snapshot(testContext)
			got, _ := snap.FindTask(task.ID)
			if !reflect.DeepEqual(got, beforeStale) {
				t.Fatal("stale turn changed task", got)
			}
			if phase != "judgement" {
				s = reopenAssetService(t, s)
				got, err = s.PrepareAssetWriting(testContext, got)
				if err != nil || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Timing" {
					t.Fatal(got, err)
				}
			}
		})
	}
}

func TestAssetReviewIdentityAndRecurrenceDoNotConflict(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, _ = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Unreachable = nil; return "", nil })
	for n := 1; n <= 2; n++ {
		for _, id := range []string{"code", "asset"} {
			u := Unreachable{ID: fmt.Sprintf("%s-%d", id, n), Source: "review", Finding: id, Criterion: "Frames", Why: id, Revision: n}
			var err error
			task, err = s.RouteAsset(testContext, task, u, "classification")
			if err != nil {
				t.Fatal(err)
			}
			no := false
			u.AssetCreation, u.Why = &no, "code correction"
			task, err = s.RecordAssetClassification(testContext, task, []Unreachable{u}, "Writer", nil)
			if err != nil {
				t.Fatal(err)
			}
		}
		if n == 1 {
			task, _ = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				t.Revisions = append(t.Revisions, Revision{N: 2, Ref: "next"})
				return "", nil
			})
		}
	}
	if len(task.Unreachable) != 4 || task.Unreachable[0].Revision != 1 || task.Unreachable[2].Revision != 2 {
		t.Fatal(task.Unreachable)
	}
}

func TestUnknownReviewRouteRetainsItsSourceWhenDesignerDisappears(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, _ = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Unreachable = nil; return "", nil })
	u := Unreachable{ID: "review-timing", Source: "review", Finding: "Timing is wrong", Criterion: "Timing", Why: "Timing is wrong", Revision: 1}
	task, err := s.RouteAsset(testContext, task, u, "classification")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Roles = slices.DeleteFunc(t.Roles, func(r Role) bool { return r.Holds(RoleDesigner) })
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	task, err = s.PrepareAssetWriting(testContext, task)
	if err != nil || task.Status != TaskDeciding || len(task.Pending(1)) != 0 || len(task.Unreachable) != 1 || task.Unreachable[0].Source != "review" || task.Unreachable[0].Why != u.Why || task.Unreachable[0].Routed != "" {
		t.Fatal(task, err)
	}
}

func TestRequirementUndoRestoresDistinctReviewFindingReports(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, _ = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Unreachable = []Unreachable{{ID: "timing", Source: "review", Criterion: "Frames", Finding: "Timing", Revision: 1}, {ID: "art", Source: "review", Criterion: "Frames", Finding: "Art", Revision: 1}}
		return "", nil
	})
	err := s.store.update(testContext, func(v *Snapshot) error {
		var out Task
		return s.applyEdit(v, taskRecordForTest(v, task.ID), TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: task.Objective, Criteria: []string{"Timing"}}}, &out)
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	got, err := s.UndoTaskEdit(testContext, task.ProjectID, task.ID, task.Edits[len(task.Edits)-1].ID)
	if err != nil || len(got.Unreachable) != 2 {
		t.Fatal(got, err)
	}
}

func TestAssetRedirectAndProductionAreAtomic(t *testing.T) {
	s, task := productionFixture(t, 1)
	task, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskDeciding
		t.Design = nil
		yes := true
		t.Unreachable = []Unreachable{{Criterion: "Frames", Revision: 0, AssetCreation: &yes}, {Criterion: "Timing", Revision: 0}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	routed, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
	if err != nil || routed.Status != TaskWriting || len(routed.Unreachable) != 2 {
		t.Fatal(routed, err)
	}
	if _, err = s.RouteAsset(testContext, task, task.Unreachable[0], "Writer"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.AskDesign(testContext, task.ID, DesignAsk{Assets: []WantedAsset{{Name: "frame"}}}); err == nil {
		t.Fatal("invalid spec accepted")
	}
	snap, _ := s.Snapshot(testContext)
	unchanged, _ := snap.FindTask(task.ID)
	if unchanged.Status != TaskWriting || len(unchanged.Unreachable) != 2 || len(unchanged.Design) != 0 {
		t.Fatal("failed request changed routed report", unchanged)
	}
	ask := DesignAsk{Requirements: []string{"Frames"}, From: "Writer", Question: "Produce frames", Assets: []WantedAsset{{Name: "frame", Want: "PNG 32x32"}}, Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}}
	for i := 0; i < 3; i++ {
		if i > 0 {
			ask.Requirements = nil
		}
		requested, e := s.AskDesign(testContext, routed.ID, ask)
		if e != nil || requested.Status != TaskDesigning || requested.DecisionID != "" {
			t.Fatal(requested, e)
		}
		if i == 0 && (len(requested.Unreachable) != 1 || requested.Unreachable[0].Criterion != "Timing" || len(requested.OpenDesign().Requirements) != 1) {
			t.Fatal(requested)
		}
		// Complete a synthetic group through the real production protocol.
		requested = startProduction(t, s, requested)
		in := productionFile(requested)
		if _, e = s.AttachAsset(testContext, in, "frame", 1); e != nil {
			t.Fatal(e)
		}
		reply := productionReply("frame")
		reply.Turn = 1
		routed, e = s.RecordDesign(testContext, requested.ID, requested.OpenDesign().ID, reply)
		if e != nil {
			t.Fatal(e)
		}
	}
	if routed.DesignInputsAt(TaskWriting) != 0 || routed.DesignsAt(TaskWriting) != 3 {
		t.Fatal(routed.Design)
	}
}

func routedDraftFixture(t *testing.T) (*Service, Task) {
	t.Helper()
	s, task := productionFixture(t, 1)
	task, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status, t.Design = TaskDeciding, nil
		t.Criteria = []string{"Frames", "Timing"}
		t.Revisions = []Revision{{N: 1, Ref: "original-draft", Summary: "Original draft"}}
		yes := true
		t.Unreachable = []Unreachable{{Criterion: "Frames", Why: "sandbox generator unavailable", Revision: 1, AssetCreation: &yes}, {Criterion: "Timing", Why: "code", Revision: 1}}
		writer, _ := t.Role("Writer")
		t.KeepThread(RoleImplementer, writer, []byte(`{"engine":"claude","id":"original"}`))
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, task
}

func TestBriefRemovalReconcilesPreparedLegacyReportsAcrossRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		s, task := routedDraftFixture(t)
		p, err := s.UpdateBrief(testContext, task.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Brief frames"}})
		if err != nil {
			t.Fatal(err)
		}
		task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
			t.Handoff = &Handoff{Name: "prepared", Revision: Revision{N: 2, Ref: "prepared-ref"}, Unreachable: []Unreachable{{Criterion: "Brief frames", Why: "generator unavailable", Revision: 2}, {Criterion: "Timing", Why: "code", Revision: 2}}}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.UpdateBrief(testContext, p.ID, BriefInput{Goal: "Assets", Criteria: []string{"Replacement"}}); err != nil {
			t.Fatal(err)
		}
		if restart {
			s = reopenAssetService(t, s)
		}
		snap, _ := s.Snapshot(testContext)
		got, _ := snap.FindTask(task.ID)
		if got.Handoff == nil || len(got.Handoff.Unreachable) != 1 || got.Handoff.Unreachable[0].Criterion != "Timing" || got.Handoff.Revision.Ref != "prepared-ref" {
			t.Fatal(got)
		}
		// A recovered commit restores only the reconciled outcome.
		got, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
			t.Unreachable = t.Handoff.Unreachable
			t.Revisions = append(t.Revisions, t.Handoff.Revision)
			t.Handoff = nil
			t.Status = TaskDeciding
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Timing" {
			t.Fatal(got)
		}
	}
}

func TestDesignerDisappearanceRestoresProductionReportsAndRetainsGroups(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.AskDesign(testContext, task.ID, DesignAsk{Expected: &task, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame-01", Want: "PNG"}, {Name: "frame-02", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	task = deliverGroup(t, s, startProduction(t, s, task), 1)
	task = startProduction(t, s, task)
	unfinished, err := s.AttachAsset(testContext, productionFile(task), "frame-02", 2)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	p := *project(&snap, task.ProjectID)
	pb := *p.Playbook
	pb.Roles = slices.DeleteFunc(slices.Clone(pb.Roles), func(r Role) bool { return r.Holds(RoleDesigner) })
	if _, err = s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskWaiting; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.UseProjectTeam(testContext, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskDesigning; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ = s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	before := snap
	filesBefore, _ := os.ReadDir(s.AttachmentsDirectory(task.ID))
	if _, err = s.RestoreUnfinishedProduction(Fenced(testContext, task.ID, "revoked"), task); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec(`CREATE TRIGGER reject_restoration BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'synthetic record failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreUnfinishedProduction(testContext, task); err == nil {
		t.Fatal("rejected restoration committed")
	}
	after, _ := s.Snapshot(testContext)
	filesAfter, _ := os.ReadDir(s.AttachmentsDirectory(task.ID))
	if !reflect.DeepEqual(before, after) || len(filesBefore) != len(filesAfter) {
		t.Fatal("failed restoration changed records or files")
	}
	if _, err = s.store.db.Exec(`DROP TRIGGER reject_restoration`); err != nil {
		t.Fatal(err)
	}
	task, err = s.RestoreUnfinishedProduction(testContext, task)
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.PrepareAssetWriting(testContext, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Unreachable) != 2 || !strings.Contains(task.Unreachable[1].Why, "no designer") || !task.NeedsAssetIntegration() || task.OpenDesign() != nil || len(task.Design[0].Production.Delivered) != 1 || len(task.Design[0].Production.Turns) != 2 {
		t.Fatal(task)
	}
	if _, err = os.ReadFile(filepath.Join(s.AttachmentsDirectory(task.ID), task.Design[0].Production.Delivered[0].Attachment)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.AttachmentsDirectory(task.ID), unfinished[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unfinished file retained", err)
	}
	for _, id := range []string{task.Design[0].Production.Provenance, task.Design[0].Production.Archive} {
		if id == "" {
			t.Fatal("interrupted hand-back lacks provenance/archive")
		}
		if _, err = os.ReadFile(filepath.Join(s.AttachmentsDirectory(task.ID), id)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPartialProductionClassificationCannotApproveOriginalDraft(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.AskDesign(testContext, task.ID, DesignAsk{Expected: &task, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame-01", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	task = deliverGroup(t, s, startProduction(t, s, task), 1)
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	no := false
	report := task.Unreachable[0]
	report.AssetCreation = &no
	task, err = s.RecordAssetClassification(testContext, task, []Unreachable{report}, "Writer", nil)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskWriting || !task.NeedsAssetIntegration() || len(task.Revisions) != 1 || task.Revisions[0].Ref != "original-draft" || task.Unreachable[0].AssetCreation == nil || *task.Unreachable[0].AssetCreation {
		t.Fatal(task)
	}
}

func reopenAssetService(t *testing.T, s *Service) *Service {
	t.Helper()
	path := filepath.Join(s.store.stateDirectory, "state.db")
	cfg, now := s.configuration(), s.now
	if err := s.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s = NewService(st, cfg)
	s.now = now
	return s
}

func TestProductionUpdatesClassificationAndSettlesOnlyLinkedReports(t *testing.T) {
	s, task := routedDraftFixture(t)
	task, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Unreachable[1].Routed = "classify decoding"
		t.Unreachable[1].Criterion = "Decoding"
		yes := true
		t.Unreachable = append(t.Unreachable, Unreachable{Criterion: "Icons", AssetCreation: &yes, Routed: "another asset route", Revision: 1})
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	no := false
	got, err := s.AskDesign(testContext, task.ID, DesignAsk{Expected: &task, From: "Writer", Assets: []WantedAsset{{Name: "frame", Want: "PNG 32x32"}}, Question: "Make a frame", Requirements: []string{"Frames"}, Reports: []Unreachable{{Criterion: "Decoding", AssetCreation: &no, Why: "owner machine", Revision: 1}}, Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.OpenDesign().Requirements, []string{"Frames"}) || len(got.Unreachable) != 2 {
		t.Fatal(got)
	}
	decode := reportFor(got.Unreachable, "Decoding")
	icons := reportFor(got.Unreachable, "Icons")
	if decode == nil || decode.AssetCreation == nil || *decode.AssetCreation || decode.Routed != "" || icons == nil || icons.Routed == "" {
		t.Fatal(got.Unreachable)
	}
}

func TestAssetRoutingStoreFailuresAndRevokedClaimsLeaveWholeRecords(t *testing.T) {
	for _, step := range []string{"redirect", "request", "classification", "brief", "stale", "cancelled"} {
		t.Run(step, func(t *testing.T) {
			s, task := routedDraftFixture(t)
			if step == "brief" {
				if _, err := s.UpdateBrief(testContext, task.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Brief frames"}}); err != nil {
					t.Fatal(err)
				}
				if err := s.store.update(testContext, func(v *Snapshot) error {
					current := &v.Tasks[slices.IndexFunc(v.Tasks, func(t Task) bool { return t.ID == task.ID })]
					current.Unreachable = append(current.Unreachable, Unreachable{Criterion: "Brief frames", Why: "sandbox", Revision: 1})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if step == "request" || step == "classification" {
				var err error
				task, err = s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := s.Snapshot(testContext)
			ctx := testContext
			if step == "stale" {
				ctx = Fenced(ctx, task.ID, "revoked-claim")
			} else if step == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			} else {
				if _, err := s.store.db.Exec(`CREATE TRIGGER reject_asset_record BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'synthetic record failure'); END;`); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch step {
			case "brief":
				_, err = s.UpdateBrief(ctx, task.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Replacement"}})
			case "request":
				_, err = s.AskDesign(ctx, task.ID, DesignAsk{Expected: &task, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}, Also: func(t *Task) {
					writer, _ := t.Role("Writer")
					t.KeepThread(RoleImplementer, writer, []byte(`{"id":"new"}`))
				}})
			case "classification":
				no := false
				_, err = s.RecordAssetClassification(ctx, task, []Unreachable{{Criterion: "Frames", AssetCreation: &no, Why: "code"}}, "Writer", []byte(`{"id":"new"}`))
			default:
				_, err = s.RouteAsset(ctx, task, task.Unreachable[0], "Writer")
			}
			if err == nil {
				t.Fatal("rejected operation succeeded")
			}
			if step == "stale" && !errors.Is(err, ErrStale) {
				t.Fatal(err)
			}
			after, _ := s.Snapshot(testContext)
			b, _ := before.FindTask(task.ID)
			a, _ := after.FindTask(task.ID)
			if !reflect.DeepEqual(b, a) || !reflect.DeepEqual(before.Activity, after.Activity) {
				t.Fatal("failed operation partially committed", a)
			}
		})
	}
}

func TestAssetRouteAndHandBackSurviveRestartWithOriginalDraftAndSession(t *testing.T) {
	s, original := routedDraftFixture(t)
	routed, err := s.RouteAsset(testContext, original, original.Unreachable[0], "Writer")
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(routed.ID)
	if task.Status != TaskWriting || task.Unreachable[0].Routed == "" || !reflect.DeepEqual(task.Revisions, original.Revisions) || !reflect.DeepEqual(task.Threads, original.Threads) {
		t.Fatal(task)
	}
	request, err := s.AskDesign(testContext, task.ID, DesignAsk{Expected: &task, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame-01", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	back := deliverGroup(t, s, startProduction(t, s, request), 1)
	s = reopenAssetService(t, s)
	snap, _ = s.Snapshot(testContext)
	task, _ = snap.FindTask(back.ID)
	prod := task.Design[0].Production
	if task.Status != TaskWriting || len(task.Unreachable) != 1 || !reflect.DeepEqual(task.Revisions, original.Revisions) || !reflect.DeepEqual(task.Threads, original.Threads) || len(prod.Delivered) != 1 || prod.Provenance == "" || prod.Archive == "" {
		t.Fatal(task)
	}
	if _, err = s.StartProductionTurn(testContext, task.ID, task.Design[0].ID, "Dee"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	count := 0
	for _, e := range snap.Activity {
		if e.Kind == "task.asset_routed" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("redirect and request must each record once", count)
	}
}

func TestAssetRouteReconcilesRequirementEditsAndDisappearedDesigner(t *testing.T) {
	for _, change := range []string{"remove", "rewrite", "team"} {
		t.Run(change, func(t *testing.T) {
			s, task := routedDraftFixture(t)
			task, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
			if err != nil {
				t.Fatal(err)
			}
			if change == "team" {
				snap, _ := s.Snapshot(testContext)
				p := project(&snap, task.ProjectID)
				pb := *p.Playbook
				pb.Roles = slices.DeleteFunc(slices.Clone(pb.Roles), func(r Role) bool { return r.Holds(RoleDesigner) })
				if _, err = s.SetPlaybook(testContext, p.ID, pb); err != nil {
					t.Fatal(err)
				}
				if _, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskWaiting; return "", nil }); err != nil {
					t.Fatal(err)
				}
				if _, err = s.UseProjectTeam(testContext, p.ID, task.ID); err != nil {
					t.Fatal(err)
				}
				task, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskWriting; return "", nil })
				if err != nil {
					t.Fatal(err)
				}
			} else {
				after := TaskText{Objective: task.Objective, Criteria: []string{"Timing"}}
				if change == "rewrite" {
					after.Criteria = append(after.Criteria, "Different frames")
				}
				err = s.store.update(testContext, func(v *Snapshot) error {
					var out Task
					return s.applyEdit(v, taskRecordForTest(v, task.ID), TaskEdit{By: FromOwner, Kind: FromOwner, After: after}, &out)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			s = reopenAssetService(t, s)
			snap, _ := s.Snapshot(testContext)
			task, _ = snap.FindTask(task.ID)
			got, err := s.PrepareAssetWriting(testContext, task)
			if err != nil {
				t.Fatal(err)
			}
			if change == "team" {
				u := reportFor(got.Unreachable, "Frames")
				if got.Status != TaskDeciding || u == nil || u.Routed != "" || u.AssetCreation == nil || !*u.AssetCreation {
					t.Fatal(got)
				}
			} else if len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Timing" {
				t.Fatal(got)
			}
		})
	}
}

func taskRecordForTest(v *Snapshot, id string) *Task {
	for i := range v.Tasks {
		if v.Tasks[i].ID == id {
			return &v.Tasks[i]
		}
	}
	return nil
}

func TestAssetRedirectRejectsEditsStopsAndConcurrentDuplicates(t *testing.T) {
	for _, change := range []string{"edit", "stop", "settle", "concurrent"} {
		t.Run(change, func(t *testing.T) {
			s, task := productionFixture(t, 1)
			task, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
				t.Status = TaskDeciding
				yes := true
				t.Unreachable = []Unreachable{{Criterion: "Frames", AssetCreation: &yes}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if change != "concurrent" {
				_, err = s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					switch change {
					case "edit":
						t.TextVersion++
					case "stop":
						t.Status = TaskStopped
					case "settle":
						t.Unreachable = nil
					}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.RouteAsset(testContext, task, task.Unreachable[0], "Writer"); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				return
			}
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := s.RouteAsset(testContext, task, task.Unreachable[0], "Writer")
					results <- err
				}()
			}
			wg.Wait()
			close(results)
			ok := 0
			for err := range results {
				if err == nil {
					ok++
				} else if !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			}
			if ok != 1 {
				t.Fatal("successful redirects", ok)
			}
		})
	}
}

func TestRetiredLinkedAssetsReleaseIntegrationAcrossRestart(t *testing.T) {
	s, current := routedDraftFixture(t)
	_, err := s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}})
	if err != nil {
		t.Fatal(err)
	}
	current, err = s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
		task.Criteria = []string{"Frames"}
		task.Design = []DesignRequest{{AnsweredAt: s.now(), IntegrationPending: true, AssetReports: []Unreachable{{Criterion: "Frames", Bound: "task"}}, Production: &Production{Delivered: []DeliveredAsset{{}}}}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.store.update(testContext, func(v *Snapshot) error {
		task := task(v, current.ID)
		var out Task
		return s.applyEdit(v, task, TaskEdit{By: FromOwner, After: TaskText{Objective: task.Objective}}, &out)
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	current, _ = snap.FindTask(current.ID)
	if !current.NeedsAssetIntegration() {
		t.Fatal("brief still requires integration")
	}
	_, err = s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets"})
	if err != nil {
		t.Fatal(err)
	}
	s = reopenAssetService(t, s)
	snap, _ = s.Snapshot(testContext)
	current, _ = snap.FindTask(current.ID)
	if current.NeedsAssetIntegration() || len(current.Design[0].AssetReports) != 1 || len(current.Design[0].Production.Delivered) != 1 {
		t.Fatal(current)
	}
}

func TestProductionRecoveryRetainsTransferUndoEvidence(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			s, current := routedDraftFixture(t)
			if _, err := s.UpdateBrief(testContext, current.ProjectID, BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
				t.Fatal(err)
			}
			current, err := s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
				t.Status = TaskWriting
				// Keep the existing draft for undo evidence.
				t.Criteria = nil
				t.Unreachable = t.Unreachable[:1]
				t.Unreachable[0].Bound = "brief"
				t.Unreachable[0].Revision = 1
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			current, err = s.AskDesign(testContext, current.ID, DesignAsk{Expected: &current, From: "Writer", Requirements: []string{"Frames"}, Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Question: "Frames", Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				err = s.store.update(testContext, func(v *Snapshot) error {
					t := task(v, current.ID)
					var out Task
					return s.applyEdit(v, t, TaskEdit{By: FromOwner, Kind: FromOwner, After: TaskText{Objective: t.Objective, OwnerChecks: []string{"Frames"}}}, &out)
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			current, err = s.UpdateTask(testContext, current.ID, func(t *Task, _ *Project) (string, error) {
				t.Roles = slices.DeleteFunc(t.Roles, func(r Role) bool { return r.Holds(RoleDesigner) })
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			s = reopenAssetService(t, s)
			snap, _ := s.Snapshot(testContext)
			current, _ = snap.FindTask(current.ID)
			current, err = s.RestoreUnfinishedProduction(testContext, current)
			if err != nil {
				t.Fatal(err)
			}
			current, err = s.PrepareAssetWriting(testContext, current)
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				if len(current.Unreachable) != 0 || !slices.Contains(current.OwnerChecks, "Frames") {
					t.Fatal(current)
				}
				if len(current.Edits[0].Settled) != 1 {
					t.Fatal(current.Edits)
				}
				current, err = s.UndoTaskEdit(testContext, current.ProjectID, current.ID, current.Edits[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(current.Unreachable) != 1 || current.Unreachable[0].Criterion != "Frames" {
					t.Fatal(current)
				}

			} else if len(current.Unreachable) != 1 || current.Unreachable[0].AssetCreation == nil || !*current.Unreachable[0].AssetCreation || !strings.Contains(current.Unreachable[0].Why, "no designer") {
				t.Fatal(current)
			}
		})
	}
}

func TestIdentityCoveredProductionHandbackAndRestart(t *testing.T) {
	for _, coverBoth := range []bool{false, true} {
		s, current := routedDraftFixture(t)
		current, err := s.UpdateTask(testContext, current.ID, func(task *Task, _ *Project) (string, error) {
			yes := true
			task.Status = TaskWriting
			task.Criteria = []string{"Animation"}
			task.Unreachable = []Unreachable{{ID: "a", Criterion: "Animation", Why: "blue", Revision: 1, AssetCreation: &yes, Routed: "designer"}, {ID: "b", Criterion: "Animation", Why: "red", Revision: 1, AssetCreation: &yes, Routed: "designer"}}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{"a"}
		if coverBoth {
			ids = append(ids, "b")
		}
		current, err = s.AskDesign(testContext, current.ID, DesignAsk{Expected: &current, From: "Writer", Question: "Frames", Requirements: ids, Assets: []WantedAsset{{Name: "frame", Want: "PNG"}}, Owner: DecisionInput{Title: "Frames", Context: "Frames", Recommendation: "Continue", Choices: []string{"Continue", "Stop"}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(current.OpenDesign().AssetReports) != len(ids) {
			t.Fatal(current)
		}
		current = startProduction(t, s, current)
		if _, err = s.AttachAsset(testContext, productionFile(current), "frame", 1); err != nil {
			t.Fatal(err)
		}
		reply := productionReply("frame")
		reply.Turn = 1
		current, err = s.RecordDesign(testContext, current.ID, current.OpenDesign().ID, reply)
		if err != nil {
			t.Fatal(err)
		}
		s = reopenAssetService(t, s)
		snap, _ := s.Snapshot(testContext)
		current, _ = snap.FindTask(current.ID)
		want := 1
		if coverBoth {
			want = 0
		}
		if len(current.Unreachable) != want || !current.NeedsAssetIntegration() || len(current.Design[0].AssetReports) != len(ids) {
			t.Fatal(current)
		}
	}
}
