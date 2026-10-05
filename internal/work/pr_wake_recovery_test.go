//go:build !windows

package work

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// rejectWakeResume fails only a state write that resumes the watched task.
// In the old multi-write watcher this lets firing commit before resume fails.
func rejectWakeResume(t *testing.T, a *Loop, taskID string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(a.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TRIGGER reject_resume BEFORE UPDATE ON state
 WHEN EXISTS (SELECT 1 FROM json_each(NEW.payload, '$.snapshot.tasks')
 WHERE json_extract(value, '$.id') = '` + taskID + `'
 AND json_extract(value, '$.status') = 'landing')
 BEGIN SELECT RAISE(ABORT, 'resume failed'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPRWakeResumeWriteFailureRetriesAfterRestart(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 8)
	task := queuePR(t, s)
	restorePRAssets(t, s)
	if _, err := s.a.Core.SendTeamMessage(s.ctx, s.p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
		t.Fatal(err)
	}
	s.gh.set(func() { s.gh.queued = false })
	before, _ := s.a.Core.Snapshot(s.ctx)
	db := rejectWakeResume(t, s.a, task.ID)
	if err := s.a.checkWakes(s.ctx, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("resume failure not returned")
	}
	after, _ := s.a.Core.Snapshot(s.ctx)
	if !reflect.DeepEqual(before.Tasks, after.Tasks) || !reflect.DeepEqual(before.Wakes, after.Wakes) {
		t.Fatal("wake fired without resuming its task", after.Wakes)
	}
	if _, err := db.Exec("DROP TRIGGER reject_resume"); err != nil {
		t.Fatal(err)
	}
	restartPR(t, s)
	if err := s.a.checkWakes(s.ctx, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	step(t, s.a)
	got := taskByID(t, s.a, task.ID)
	if got.Status != core.TaskWriting || got.PRMergePending() || !got.NeedsAssetIntegration() || got.DirectionPending != 1 || len(got.Revisions) != 1 || len(s.gh.merges) != 1 {
		t.Fatal("resume did not recover integration", got)
	}
}

func TestPRWakeRestartRecoversBothInterruptionOrders(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"fire-first", "resume-first"} {
		for _, continuation := range []string{"assets", "policy-switch"} {
			t.Run(order+"/"+continuation, func(t *testing.T) {
				s := newPRScenario(t, 8)
				task := queuePR(t, s)
				if continuation == "assets" {
					restorePRAssets(t, s)
					if _, err := s.a.Core.SendTeamMessage(s.ctx, s.p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.a.Core.UpdateTask(s.ctx, task.ID, func(_ *core.Task, p *core.Project) (string, error) {
						p.Settings.Land.PullRequests = false
						p.PRChoices = []string{task.ID}
						return "", nil
					}); err != nil {
						t.Fatal(err)
					}
					if _, err := s.a.Core.ApplyPM(s.ctx, s.p.ID, core.PMAnswer{PRFlow: []core.PRFlowChoice{{Task: task.ID}}}); err != nil {
						t.Fatal(err)
					}
					pending := taskByID(t, s.a, task.ID)
					if pending.PRSwitchBy == "" || !pending.UsesPRs() || !pending.PRMergePending() {
						t.Fatal("policy switch did not wait for merge reconciliation", pending)
					}
				}
				// A wake observed while landing is mid-step remains fired. The old
				// daemon could then leave Awaiting as its final write before stopping.
				if err := s.a.setStatus(s.ctx, task.ID, core.TaskLanding, "mid-step"); err != nil {
					t.Fatal(err)
				}
				if order == "fire-first" {
					snap, _ := s.a.Core.Snapshot(s.ctx)
					count := 0
					for _, w := range snap.Wakes {
						if w.TaskID == task.ID && w.Owner == core.WakeLoop && w.Status == core.WakeWaiting {
							if _, err := s.a.Core.FireWake(s.ctx, w.ID, "cancelled", "Merge cancelled", false); err != nil {
								t.Fatal(err)
							}
							count++
						}
					}
					if count != 2 {
						t.Fatal("fixture needs both PR watches", count)
					}
					if err := s.a.setStatus(s.ctx, task.ID, core.TaskAwaiting, "last mid-step write"); err != nil {
						t.Fatal(err)
					}
				}
				s.gh.set(func() { s.gh.queued = false })
				restartPR(t, s)
				step(t, s.a)
				claims, err := s.a.Core.Schedule(s.ctx, func(core.Role) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				got := taskByID(t, s.a, task.ID)
				if got.PRMergePending() || len(got.Revisions) != 1 || len(s.gh.merges) != 1 {
					t.Fatal("lost or repeated merge", got)
				}
				if continuation == "assets" {
					if got.Status != core.TaskWriting || !got.NeedsAssetIntegration() || got.DirectionPending != 1 {
						t.Fatal("stranded restored work", got)
					}
				} else if got.Status != core.TaskDeciding || got.UsesPRs() || got.Proposal != nil || got.PRSwitchBy != "" {
					t.Fatal("stranded policy switch", got)
				}
				if continuation == "policy-switch" {
					snap, err := s.a.Core.Snapshot(s.ctx)
					if err != nil {
						t.Fatal(err)
					}
					project, ok := findProject(snap, s.p.ID)
					if !ok || !reflect.DeepEqual(got.Playbook.Land, project.Playbook.Land) || got.Approved != 0 || got.ClosePR == nil || got.ClosePR.Number != task.Proposal.Number {
						t.Fatal("policy switch did not adopt the project policy and retire PR approval", got)
					}
				}
				for _, c := range claims {
					if err := s.a.Core.ReleaseClaim(s.ctx, c.Task.ID, c.Claim.Token); err != nil {
						t.Fatal(err)
					}
				}
				if order == "fire-first" {
					snap, _ := s.a.Core.Snapshot(s.ctx)
					for _, w := range snap.Wakes {
						if w.TaskID == task.ID && w.Status == core.WakeFired {
							t.Fatal("replay left fired wake", w)
						}
					}
				}
			})
		}
	}
}
