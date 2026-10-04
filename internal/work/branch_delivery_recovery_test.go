//go:build !windows

package work

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
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
				if held, err := a.beginDelivering(ctx, task.ID, r); err != nil || len(held) != 0 {
					t.Fatal(held, err)
				}
				m, task := a.testMedium(t, p.ID, task.ID)
				if numbered {
					ownerGit(t, p.Playbook.Repo, "update-ref", "refs/heads/"+m.branchName(task), task.Base)
				}
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
