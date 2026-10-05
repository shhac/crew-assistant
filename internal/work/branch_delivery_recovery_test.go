//go:build !windows

package work

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

func TestBranchDeliveryRestartRetainsActualDestination(t *testing.T) {
	for _, numbered := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			name := map[bool]string{false: "base", true: "numbered"}[numbered] + map[bool]string{false: "/active", true: "/stopped"}[stopped]
			t.Run(name, func(t *testing.T) {
				a, _, p, task := codeTask(t, pass, pass)
				ctx := context.Background()
				settle(t, a)
				task = taskByID(t, a, task.ID)
				r := task.Revisions[len(task.Revisions)-1]
				if _, err := a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
					task.Status, task.DecisionID, task.Approved = core.TaskLanding, "", r.N
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				m, task := a.testMedium(t, p.ID, task.ID)
				if numbered {
					ownerGit(t, p.Playbook.Repo, "update-ref", "refs/heads/"+m.branchName(task), task.Base)
				}
				destination, err := m.repo.Destination(ctx, r.Ref, m.branchName(task))
				if err != nil {
					t.Fatal(err)
				}
				if held, err := a.beginDelivering(ctx, task.ID, r, destination); err != nil || len(held) != 0 {
					t.Fatal(held, err)
				}
				task = taskByID(t, a, task.ID)
				branch, err := m.deliver(ctx, task, r)
				if err != nil {
					t.Fatal(err)
				}
				if numbered && branch != m.branchName(task)+"-2" {
					t.Fatal("fixture did not use a numbered destination", branch)
				}
				if there, err := m.alreadyLanded(ctx, task, r); err != nil || !there {
					t.Fatal("branch landing predicate lost delivery evidence", there, err)
				}
				branches := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
				// The branch went out, but its record did not commit. New work
				// cannot erase that outcome or change which draft it represents.
				if _, err := a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
					task.Design = []core.DesignRequest{{ID: "frames", AnsweredAt: time.Now(), IntegrationSuspended: true, AssetReports: []core.Unreachable{{ID: "frames", Criterion: "Frames", Bound: "brief"}}, Production: &core.Production{Delivered: []core.DeliveredAsset{{Attachment: "asset"}}, Provenance: "provenance"}}}
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Core.UpdateBrief(ctx, p.ID, core.BriefInput{Goal: p.Brief.Goal, Criteria: []string{"Frames"}}); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Core.SendTeamMessage(ctx, p.ID, task.ID, "implementer", core.FromOwner, "Integrate Frames"); err != nil {
					t.Fatal(err)
				}
				if stopped {
					if _, err := a.StopTask(ctx, p.ID, task.ID); err != nil {
						t.Fatal(err)
					}
				}
				a = restart(t, a)
				step(t, a)
				got := taskByID(t, a, task.ID)
				if got.Status != core.TaskDelivered || got.DeliveredTo != branch || got.Delivering != nil || len(got.Revisions) != 1 || !got.NeedsAssetIntegration() || got.DirectionPending != 1 {
					t.Fatal("restart lost original branch delivery", got)
				}
				snap, _ := a.Core.Snapshot(ctx)
				project, _ := findProject(snap, p.ID)
				if project.Landed == nil || project.Landed.Branch != branch || project.Landed.Commit != r.Ref {
					t.Fatal("landing attributed to another destination or draft", project.Landed)
				}
				step(t, a)
				if got := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads"); got != branches {
					t.Fatal("reconciliation created another branch", got)
				}
			})
		}
	}
}

// interruptedBranch stops after ref creation but before recording its outcome.
func interruptedBranch(t *testing.T, numbered bool, objective string) (*Loop, core.Project, core.Task, string) {
	t.Helper()
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	settle(t, a)
	task = taskByID(t, a, task.ID)
	var err error
	if objective != "" {
		task, err = a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RolePM, By: "PM", Objective: objective})
		if err != nil {
			t.Fatal(err)
		}
	}
	task, err = a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Status, task.DecisionID, task.Approved = core.TaskLanding, "", 1
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, task := a.testMedium(t, p.ID, task.ID)
	if numbered {
		ownerGit(t, p.Playbook.Repo, "branch", m.branchName(task), task.Base)
	}
	branch, err := m.repo.Destination(ctx, task.Revisions[0].Ref, m.branchName(task))
	if err != nil {
		t.Fatal(err)
	}
	if held, err := a.beginDelivering(ctx, task.ID, task.Revisions[0], branch); err != nil || len(held) != 0 {
		t.Fatal(held, err)
	}
	task = taskByID(t, a, task.ID)
	if actual, err := m.deliver(ctx, task, task.Revisions[0]); err != nil || actual != branch {
		t.Fatal(actual, err)
	}
	return a, p, task, branch
}

func TestBranchRestartIgnoresObjectiveEditAndUndo(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(map[bool]string{false: "edit", true: "undo"}[undo], func(t *testing.T) {
			a, p, task, branch := interruptedBranch(t, false, "Delivery objective")
			ctx := context.Background()
			var err error
			if undo {
				_, err = a.Core.UndoTaskEdit(ctx, p.ID, task.ID, task.Edits[len(task.Edits)-1].ID)
			} else {
				_, err = a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RolePM, By: "PM", Objective: "A different objective"})
			}
			if err != nil {
				t.Fatal(err)
			}
			branches := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
			a = restart(t, a)
			step(t, a)
			got := taskByID(t, a, task.ID)
			if got.Status != core.TaskDelivered || got.DeliveredTo != branch || got.Delivering != nil || len(got.Revisions) != 1 {
				t.Fatal("lost delivery after text change", got)
			}
			snap, _ := a.Core.Snapshot(ctx)
			project, _ := findProject(snap, p.ID)
			if project.Landed == nil || project.Landed.Branch != branch || project.Landed.Commit != task.Revisions[0].Ref {
				t.Fatal(project.Landed)
			}
			step(t, a)
			if refs := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads"); refs != branches {
				t.Fatal("delivered twice", refs)
			}
		})
	}
}

func TestBranchRestartFindsNumberedDestinationWithoutBase(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "legacy"}[legacy], func(t *testing.T) {
			a, p, task, branch := interruptedBranch(t, true, "")
			ctx := context.Background()
			m, _ := a.testMedium(t, p.ID, task.ID)
			ownerGit(t, p.Playbook.Repo, "branch", "-D", m.branchName(task))
			if legacy {
				if _, err := a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
					task.Delivering.Branch = ""
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			a = restart(t, a)
			step(t, a)
			got := taskByID(t, a, task.ID)
			if got.Status != core.TaskDelivered || got.DeliveredTo != branch || len(got.Revisions) != 1 {
				t.Fatal("lost numbered delivery", got)
			}
			if refs := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname)", "refs/heads/paul"); refs != "refs/heads/"+branch {
				t.Fatal("created another branch", refs)
			}
		})
	}
}

func TestBranchRestartRetainsUnknownDestinationUntilRepair(t *testing.T) {
	for _, failure := range []string{"corrupt", "unreadable", "unexpected-tip"} {
		t.Run(failure, func(t *testing.T) {
			a, p, task, branch := interruptedBranch(t, false, "")
			ctx := context.Background()
			ref := filepath.Join(p.Playbook.Repo, ".git", "refs", "heads", branch)
			switch failure {
			case "corrupt":
				if err := os.WriteFile(ref, []byte("broken\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				if err := os.Rename(p.Playbook.Repo, p.Playbook.Repo+"-away"); err != nil {
					t.Fatal(err)
				}
			case "unexpected-tip":
				ownerGit(t, p.Playbook.Repo, "update-ref", "refs/heads/"+branch, task.Base)
			}
			a = restart(t, a)
			step(t, a)
			got := taskByID(t, a, task.ID)
			if got.Delivering == nil || got.Delivering.Branch != branch || got.Status != core.TaskWaiting || len(got.Revisions) != 1 {
				t.Fatal("unknown outcome released", got)
			}
			d := openDecision(t, a, got)
			if d.Kind != core.DecisionFailure {
				t.Fatal("missing failure decision", d)
			}
			if failure == "unexpected-tip" && (!strings.Contains(d.Context, "restore its expected commit") || !strings.Contains(d.Context, "Try again")) {
				t.Fatal("missing owner recovery guidance", d.Context)
			}
			switch failure {
			case "unreadable":
				if err := os.Rename(p.Playbook.Repo+"-away", p.Playbook.Repo); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(ref, []byte(task.Revisions[0].Ref+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			branches := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
			if _, err := a.Core.ChooseDecision(ctx, d.ID, choiceTryAgain, core.FromOwner); err != nil {
				t.Fatal(err)
			}
			settle(t, a)
			got = taskByID(t, a, task.ID)
			if got.Delivering != nil || got.Status != core.TaskDelivered || got.DeliveredTo != branch || len(got.Revisions) != 1 {
				t.Fatal("repair failed to settle", got)
			}
			if refs := ownerGit(t, p.Playbook.Repo, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads"); refs != branches {
				t.Fatal("repair redelivered", refs)
			}
		})
	}
}

func TestStoppedUnknownBranchDoesNotBlockOtherProjects(t *testing.T) {
	for _, failure := range []string{"corrupt", "unreadable"} {
		t.Run(failure, func(t *testing.T) {
			a, p, task, branch := interruptedBranch(t, false, "")
			ctx := context.Background()
			if _, err := a.StopTask(ctx, p.ID, task.ID); err != nil {
				t.Fatal(err)
			}
			ref := filepath.Join(p.Playbook.Repo, ".git", "refs", "heads", branch)
			if failure == "corrupt" {
				if err := os.WriteFile(ref, []byte("broken\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(p.Playbook.Repo, p.Playbook.Repo+"-away"); err != nil {
				t.Fatal(err)
			}
			healthy := codeProject(t, a, ownerRepo(t))
			next, err := a.Core.QueueTask(ctx, healthy.ID, core.TaskInput{Objective: "Other project"})
			if err != nil {
				t.Fatal(err)
			}
			var logged bytes.Buffer
			a = restart(t, a)
			a.Diagnostics = diagnostics.New(&logged)
			step(t, a)
			got := taskByID(t, a, task.ID)
			if got.Status != core.TaskStopped || got.Delivering == nil || got.Delivering.Branch != branch {
				t.Fatal("released stopped uncertain intent", got)
			}
			if got := taskByID(t, a, next.ID); got.Status == core.TaskQueued {
				t.Fatal("unreadable task blocked scheduling", got)
			}
			if !strings.Contains(logged.String(), "delivery_recovery") {
				t.Fatal("settlement failure not reported", logged.String())
			}
			before := logged.String()
			snap, _ := a.Core.Snapshot(ctx)
			if err := a.settleDeliveries(ctx, snap); err != nil {
				t.Fatal(err)
			}
			if logged.String() != before {
				t.Fatal("unchanged failure logged again", logged.String())
			}
			// A successful observation resets suppression for a later failure.
			if failure == "unreadable" {
				if err := os.Rename(p.Playbook.Repo+"-away", p.Playbook.Repo); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(ref, []byte(task.Revisions[0].Ref+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := a.settleDeliveries(ctx, snap); err != nil {
				t.Fatal(err)
			}
			if _, retained := a.deliveryRecovery.Load(task.ID); retained {
				t.Fatal("successful recovery retained suppression")
			}
		})
	}
}

// takenBranchMedium injects the race between destination selection and creation.
type takenBranchMedium struct {
	gitMedium
	test *testing.T
}

func (m takenBranchMedium) deliver(_ context.Context, task core.Task, _ core.Revision) (string, error) {
	ownerGit(m.test, m.playbook.Repo, "branch", m.branchName(task), task.Base)
	return "", gitrepo.ErrBranchTaken
}

func TestTakenBranchRetriesWithoutOwnerDecision(t *testing.T) {
	a, _, p, task := codeTask(t, pass, pass)
	ctx := context.Background()
	settle(t, a)
	task = taskByID(t, a, task.ID)
	r := task.Revisions[0]
	var err error
	task, err = a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Status, task.DecisionID, task.Approved = core.TaskLanding, "", r.N
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, task := a.testMedium(t, p.ID, task.ID)
	if err := a.land(ctx, p, task, takenBranchMedium{m, t}); err != nil {
		t.Fatal(err)
	}
	got := taskByID(t, a, task.ID)
	if got.Status != core.TaskLanding || got.Delivering != nil || got.DecisionID != "" {
		t.Fatal("confirmed race asked the owner", got)
	}
	if err := a.land(ctx, p, got, m); err != nil {
		t.Fatal(err)
	}
	got = taskByID(t, a, task.ID)
	if got.Status != core.TaskDelivered || got.DeliveredTo != m.branchName(task)+"-2" || len(got.Revisions) != 1 {
		t.Fatal("race did not retry at numbered branch", got)
	}
	if tip := ownerGit(t, p.Playbook.Repo, "rev-parse", m.branchName(task)); tip != task.Base {
		t.Fatal("race moved owner's branch", tip)
	}
}

func TestRefCreationRefusalOpensOwnerDecision(t *testing.T) {
	for _, cause := range []string{"child-ref", "stale-lock"} {
		t.Run(cause, func(t *testing.T) {
			a, _, p, task := codeTask(t, pass, pass)
			ctx := context.Background()
			settle(t, a)
			task = taskByID(t, a, task.ID)
			task, err := a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Status, task.DecisionID, task.Approved = core.TaskLanding, "", task.Revisions[0].N
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, task := a.testMedium(t, p.ID, task.ID)
			branch := m.branchName(task)
			if cause == "child-ref" {
				ownerGit(t, p.Playbook.Repo, "branch", branch+"/child", task.Base)
			} else {
				lock := filepath.Join(p.Playbook.Repo, ".git", "refs", "heads", branch+".lock")
				if err := os.MkdirAll(filepath.Dir(lock), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(lock, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.land(ctx, p, task, m); err != nil {
				t.Fatal(err)
			}
			got := taskByID(t, a, task.ID)
			if got.Status != core.TaskWaiting || got.DecisionID == "" || got.Delivering != nil {
				t.Fatal("creation refusal silently retries", got)
			}
		})
	}
}

func TestRecoveryDiagnosticsPruneRemovedAndClearedTasks(t *testing.T) {
	a, _, _, task := codeTask(t, pass, pass)
	for _, snap := range []core.Snapshot{{}, {Tasks: []core.Task{task}}} {
		a.deliveryRecovery.Store(task.ID, "failure")
		if err := a.settleDeliveries(context.Background(), snap); err != nil {
			t.Fatal(err)
		}
		if _, retained := a.deliveryRecovery.Load(task.ID); retained {
			t.Fatal("stale diagnostic retained")
		}
	}
}
