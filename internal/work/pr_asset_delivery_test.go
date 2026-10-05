//go:build !windows

package work

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

func queuePR(t *testing.T, s *prScenario) core.Task {
	t.Helper()
	task := s.open(t)
	s.gh.set(func() { s.gh.checks, s.gh.decision = "SUCCESS", "APPROVED" })
	s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
			s.gh.set(func() { s.gh.queued = true; s.gh.merges = append(s.gh.merges, args) })
			return nil, nil
		}
		return s.gh.run(ctx, args...)
	}}
	if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Ready"); err != nil {
		t.Fatal(err)
	}
	return s.current(t)
}

func restartPR(t *testing.T, s *prScenario) {
	t.Helper()
	s.a = restart(t, s.a)
	s.a.github, s.a.githubURL = github.Client{Run: s.gh.run}, func(string) string { return s.remote }
}

func restorePRAssets(t *testing.T, s *prScenario) {
	t.Helper()
	seatDesigner(t, s.a, s.p.ID)
	if _, err := s.a.Core.UpdateTask(s.ctx, s.id, func(task *core.Task, _ *core.Project) (string, error) {
		task.Design = []core.DesignRequest{{ID: "frames", AnsweredAt: time.Now(), IntegrationSuspended: true, AssetReports: []core.Unreachable{{ID: "frames", Criterion: "Frames", Bound: "brief"}}, Production: &core.Production{Delivered: []core.DeliveredAsset{{Attachment: "asset"}}, Provenance: "provenance"}}}
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.a.Core.UpdateBrief(s.ctx, s.p.ID, core.BriefInput{Goal: "Assets", Criteria: []string{"Frames"}}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedMergeDefersDirectionAndAssetDraftThroughRestart(t *testing.T) {
	s := newPRScenario(t, 8)
	task := queuePR(t, s)
	restorePRAssets(t, s)
	if _, err := s.a.Core.SendTeamMessage(s.ctx, s.p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
		t.Fatal(err)
	}
	if got := taskByID(t, s.a, task.ID); got.Status != core.TaskAwaiting || got.DirectionPending != 1 {
		t.Fatal("direction started a draft before reconciliation", got)
	}
	restartPR(t, s)
	got := s.current(t)
	if got.Status != core.TaskAwaiting || len(got.Revisions) != 1 || !got.NeedsAssetIntegration() || got.DirectionPending != 1 || len(s.gh.merges) != 1 {
		t.Fatal("rewrote an in-flight merge", got)
	}
	s.gh.set(func() { s.gh.merged = task.Revisions[0].Ref })
	if err := s.a.checkWakes(s.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got = s.current(t)
	if got.Status != core.TaskLanded || len(got.Revisions) != 1 || !got.NeedsAssetIntegration() || len(s.gh.merges) != 1 {
		t.Fatal("wrong merge outcome", got)
	}
}

func TestStoppedQueuedMergeReconcilesAfterRestart(t *testing.T) {
	for _, mergedBeforeStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "merged"}[mergedBeforeStop], func(t *testing.T) {
			s := newPRScenario(t, 4)
			task := queuePR(t, s)
			if mergedBeforeStop {
				s.gh.set(func() { s.gh.merged = task.Revisions[0].Ref })
			}
			if _, err := s.a.StopTask(s.ctx, s.p.ID, task.ID); err != nil {
				t.Fatal(err)
			}
			restartPR(t, s)
			got := s.current(t)
			if !mergedBeforeStop {
				if got.Status != core.TaskStopped || !got.PRMergePending() {
					t.Fatal("forgot queued stopped merge", got)
				}
				s.gh.set(func() { s.gh.merged = task.Revisions[0].Ref })
				got = s.current(t)
			}
			if got.Status != core.TaskLanded || got.PRMergePending() || len(s.gh.merges) != 1 {
				t.Fatal("stopped merge not settled", got)
			}
		})
	}
}

func TestMergedObservationUsesRequestedDraft(t *testing.T) {
	s := newPRScenario(t, 4)
	task := queuePR(t, s)
	requested := task.Revisions[0]
	// Even inconsistent older state must not attribute an outward action to
	// a later draft. Normal edit paths are fenced against creating this state.
	task, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Revisions = append(task.Revisions, core.Revision{N: 2, Ref: "unpublished"})
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := gitMedium{playbook: *task.Playbook}
	if err := s.a.reactTo(s.ctx, s.p, task, m, task.Revisions[1], *task.Proposal, github.PR{State: "MERGED", HeadRefOid: requested.Ref, MergeCommit: &struct {
		Oid string `json:"oid"`
	}{Oid: requested.Ref}}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.a.Core.Snapshot(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snap.Projects {
		if p.ID == s.p.ID && (p.Landed == nil || p.Landed.Commit != requested.Ref) {
			t.Fatal("merge attributed to unpublished draft", p.Landed)
		}
	}
}

func TestUnreadyPRReleasesAbsentMergeBeforeAssetIntegration(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(map[bool]string{false: "intent", true: "acknowledged"}[acknowledged], func(t *testing.T) {
			s := newPRScenario(t, 8)
			task := s.open(t)
			if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Ready"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.a.beginDelivering(s.ctx, task.ID, task.Revisions[0]); err != nil {
				t.Fatal(err)
			}
			if acknowledged {
				if err := s.a.acknowledgeMerge(s.ctx, task.ID, task.Revisions[0], task.Proposal.Number); err != nil {
					t.Fatal(err)
				}
			}
			restorePRAssets(t, s)
			restartPR(t, s)
			// Run the actual scheduler and landing step, stopping before the writer.
			step(t, s.a)
			got := taskByID(t, s.a, task.ID)
			if got.Status != core.TaskWriting || got.Delivering != nil || got.PRMergePending() || !got.NeedsAssetIntegration() || len(s.gh.merges) != 0 {
				t.Fatal("unready PR stranded integration", got)
			}
		})
	}
}

func TestQueuedMergeRecoversRejectedAcknowledgementWithoutResubmit(t *testing.T) {
	s := newPRScenario(t, 4)
	task := s.open(t)
	s.gh.set(func() { s.gh.checks, s.gh.decision = "SUCCESS", "APPROVED" })
	db, err := sql.Open("sqlite", filepath.Join(s.a.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s.a.github = github.Client{Run: func(ctx context.Context, args ...string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "pr merge ") {
			s.gh.set(func() { s.gh.queued = true; s.gh.merges = append(s.gh.merges, args) })
			_, err := db.Exec(`CREATE TRIGGER reject_ack BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT, 'ack failed'); END;`)
			return nil, err
		}
		return s.gh.run(ctx, args...)
	}}
	if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "Ready"); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.a.Core.Snapshot(s.ctx)
	task, _ = snap.FindTask(task.ID)
	m, err := s.a.mediumFor(s.ctx, s.p, task.Playbook)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.a.landPR(s.ctx, s.p, task, m.(gitMedium)); err == nil {
		t.Fatal("ack write did not fail")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_ack`); err != nil {
		t.Fatal(err)
	}
	got := taskByID(t, s.a, task.ID)
	if got.Delivering == nil || got.PRMergePending() {
		t.Fatal("wrong interrupted state", got)
	}
	restartPR(t, s)
	got = s.current(t)
	if got.Status != core.TaskAwaiting || !got.PRMergePending() || got.Delivering != nil || len(s.gh.merges) != 1 {
		t.Fatal("resubmitted queued request after rejected acknowledgement", got)
	}
}
