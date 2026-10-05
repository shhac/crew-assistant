//go:build !windows

package work

import (
	"context"
	"fmt"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/roles"
	"os"
	"path/filepath"
	"testing"
)

func ownerAssetFixture(t *testing.T) (*Loop, *codeRunner, core.Project, core.Task) {
	t.Helper()
	a, runner, p, task := codeTask(t, pass, pass, pass)
	seatDesignerOn(t, a, p.ID, "codex")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		yes := true
		t.Status = core.TaskWriting
		t.Criteria = []string{"Frames"}
		t.TeamKept = []string{"Frames"}
		t.Unreachable = []core.Unreachable{{ID: "frames-report", Criterion: "Frames", Why: "generator", Revision: 1, AssetCreation: &yes, Routed: "designer"}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := a.testMedium(t, p.ID, task.ID)
	writer := task.RolesOf(core.RoleImplementer)[0]
	if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: PNG\n\nRequirement: Frames\n```"}, 0, nil); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	a = restart(t, a)
	a.runner = runner
	runner.designs = []string{productionJSON(1, 1)}
	runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, true); return nil }
	designer, _ := task.Designer()
	if err = a.design(ctx, p, task, m, designer); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	if len(task.Design) != 1 || !task.NeedsAssetIntegration() || len(task.Unreachable) != 0 || task.Design[0].AssetReports[0].ID != "frames-report" {
		t.Fatal(task)
	}
	return a, runner, p, task
}

func TestOwnerAssetAdoptionRequiresChangedByteIdenticalCoverage(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"complete", "reviewed complete", "existing coverage unrelated", "unrelated", "changed asset", "missing provenance", "unchanged", "unchanged assets", "hash mismatch", "missing attachment", "reactivated", "suspended"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, runner, p, task := ownerAssetFixture(t)
			ctx := context.Background()
			if mode == "reactivated" || mode == "suspended" {
				var err error
				task, err = a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RolePM, By: "Owner", Criteria: []string{}})
				if err != nil {
					t.Fatal(err)
				}
				if !task.Design[0].IntegrationSuspended {
					t.Fatal(task)
				}
				a = restart(t, a)
				a.runner = runner
				if mode == "reactivated" {
					task, err = a.Core.UndoTaskEdit(ctx, p.ID, task.ID, task.Edits[len(task.Edits)-1].ID)
					if err != nil {
						t.Fatal(err)
					}
					if !task.NeedsAssetIntegration() {
						t.Fatal(task)
					}
				}
			}
			place, err := a.Place(ctx, p.ID, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = gitrepo.CheckoutDraft(ctx, place.Repo, place.Records, place.Draft.Ref, place.Branch, false); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(t.TempDir(), "owner")
			if err = gitrepo.AddWorktree(ctx, place.Repo, dir, place.Branch); err != nil {
				t.Fatal(err)
			}
			prod := task.Design[0].Production
			if mode != "unchanged" {
				if err = os.WriteFile(filepath.Join(dir, "owner.txt"), []byte("owner change"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode != "unrelated" {
					for name, id := range map[string]string{"frame.png": prod.Delivered[0].Attachment, "provenance.json": prod.Provenance} {
						if mode == "missing provenance" && name == "provenance.json" {
							continue
						}
						data, err := os.ReadFile(filepath.Join(a.Core.AttachmentsDirectory(task.ID), id))
						if err != nil {
							t.Fatal(err)
						}
						if mode == "changed asset" && name == "frame.png" {
							data = append(data, '!')
						}
						if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				ownerGit(t, dir, "add", ".")
				ownerGit(t, dir, "commit", "-q", "-m", "Owner integrates")
			}
			if mode == "unchanged assets" || mode == "existing coverage unrelated" {
				same := ownerGit(t, dir, "rev-parse", "HEAD")
				task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Revisions[0].Ref = same
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "existing coverage unrelated" {
				if err = os.WriteFile(filepath.Join(dir, "owner.txt"), []byte("unrelated second change"), 0600); err != nil {
					t.Fatal(err)
				}
				ownerGit(t, dir, "add", ".")
				ownerGit(t, dir, "commit", "-q", "-m", "Unrelated edit")
			}
			if mode == "hash mismatch" {
				task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Design[0].Production.Delivered[0].SHA256 = "bad"
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing attachment" {
				if err = os.Remove(filepath.Join(a.Core.AttachmentsDirectory(task.ID), prod.Delivered[0].Attachment)); err != nil {
					t.Fatal(err)
				}
			}
			turns := len(runner.seen)
			got, err := a.AdoptDraft(ctx, p.ID, task.ID, "", "Owner draft", mode != "reviewed complete")
			if err != nil {
				t.Fatal(err)
			}
			settles := mode == "complete" || mode == "reviewed complete" || mode == "reactivated" || mode == "suspended"
			if got.Design[0].IntegrationPending == settles && mode != "suspended" {
				t.Fatal(got.Design)
			}
			if settles && got.Design[0].IntegrationSuspended {
				t.Fatal(got.Design)
			}
			wantApproved := 2
			if mode == "reviewed complete" {
				wantApproved = 0
			}
			if got.Approved != wantApproved || got.Status != core.TaskReviewing {
				t.Fatal(got)
			}
			a = restart(t, a)
			a.runner = runner
			snap, _ := a.Core.Snapshot(ctx)
			got, _ = snap.FindTask(task.ID)
			if settles && (got.Design[0].IntegrationPending || got.Design[0].IntegrationSuspended) {
				t.Fatal(got)
			}
			got = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status != core.TaskReviewing })
			if len(runner.seen) <= turns {
				t.Fatal("owner approval skipped QA")
			}
			if mode == "reviewed complete" {
				got = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskWaiting })
				var reviewer, qa bool
				for _, spec := range runner.seen[turns:] {
					reviewer = reviewer || spec.Role == core.RoleReviewer
					qa = qa || spec.Role == core.RoleQA
				}
				if !reviewer || !qa {
					t.Fatal("owner draft bypassed ordinary review or QA", runner.seen[turns:])
				}
				if got.Status != core.TaskWaiting || got.Approved != 0 {
					t.Fatal("ordinary owner draft bypassed landing approval", got)
				}
				if _, err = a.Core.ChooseDecision(ctx, got.DecisionID, choiceApprove, core.FromOwner); err != nil {
					t.Fatal(err)
				}
			}
			if settles {
				got = settleCode(t, a, task.ID)
				if got.Status != core.TaskDelivered {
					t.Fatal(got)
				}
			} else {
				if err = a.approve(ctx, got); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				got, _ = snap.FindTask(task.ID)
				if got.Status != core.TaskWriting || !got.NeedsAssetIntegration() {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestKeptUnroutedNoChangeRetainsRoleFailure(t *testing.T) {
	t.Parallel()
	for _, designer := range []bool{false, true} {
		t.Run(fmt.Sprint(designer), func(t *testing.T) {
			t.Parallel()
			a, _, p, task := codeTask(t, pass, pass)
			if designer {
				seatDesigner(t, a, p.ID)
			}
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames"}
				t.TeamKept = []string{"Frames"}
				t.Unreachable = nil
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := a.testMedium(t, p.ID, task.ID)
			if err = m.reset(ctx, task); err != nil {
				t.Fatal(err)
			}
			reply := "```owner-step\n" + `[{"requirement":"Frames","why":"code","asset_creation":false}]` + "\n```"
			if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if got.Status != core.TaskWriting || got.Failures != 1 || len(got.Revisions) != 1 || len(got.Unreachable) != 0 {
				t.Fatal(got)
			}
		})
	}
}
