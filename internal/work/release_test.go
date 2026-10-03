//go:build !windows

package work

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

type releaseRunner struct {
	scriptedRunner
	checks  int
	specs   []roles.Spec
	before  func(roles.Spec)
	verdict string
}

func (r *releaseRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	r.specs = append(r.specs, spec)
	if strings.HasPrefix(spec.Prompt, "You keep the to-do list") {
		return roles.Result{Text: `{"release":{"version":"v1.1.0","notes":"Adds a user-visible feature."},"note":"Feature landed"}`}, nil
	}
	if !strings.HasPrefix(spec.Prompt, "Check release") {
		return roles.Result{}, fmt.Errorf("unexpected turn: %s", spec.Prompt)
	}
	r.checks++
	if r.before != nil {
		r.before(spec)
	}
	_, command, _ := strings.Cut(spec.Prompt, "once:\n")
	command, _, _ = strings.Cut(command, "\n")
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = spec.Read[len(spec.Read)-1]
	cmd.Env = append(os.Environ(), spec.Env...)
	output, err := cmd.CombinedOutput()
	if r.verdict != "" {
		return roles.Result{Text: r.verdict}, nil
	}
	status := 0
	outcome := "pass"
	if err != nil {
		status = 1
		if e, ok := err.(*exec.ExitError); ok {
			status = e.ExitCode()
		}
		outcome = "fail"
	}
	return roles.Result{Text: fmt.Sprintf(`{"outcome":%q,"exit_status":%d,"output":%q}`, outcome, status, string(output))}, nil
}
func releaseFixture(t *testing.T, approve string, pr bool, remoteNamed bool, status int) (*Loop, core.Project, *releaseRunner, string, string, string) {
	t.Helper()
	ctx := context.Background()
	runner := &releaseRunner{}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	source := ownerRepo(t)
	ownerGit(t, source, "tag", "v1.0.0")
	remote := filepath.Join(t.TempDir(), "github.git")
	ownerGit(t, source, "clone", "--bare", source, remote)
	body := fmt.Sprintf("echo checked-$1\necho tail-evidence\nexit %d\n", status)
	if err := os.WriteFile(filepath.Join(source, "check.sh"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	ownerGit(t, source, "add", "check.sh")
	ownerGit(t, source, "commit", "-qm", "User-visible feature")
	tip := ownerGit(t, source, "rev-parse", "HEAD")
	if pr {
		ownerGit(t, source, "push", remote, "main")
	}
	p := codeProject(t, a, source)
	land := core.LandPolicy{Via: core.LandPush, Target: "main"}
	if pr {
		land = core.LandPolicy{PullRequests: true, GitHub: "owner/repo", Target: "main"}
	}
	var err error
	p, err = a.SetLanding(ctx, p.ID, land)
	if err != nil {
		t.Fatal(err)
	}
	policy := &core.ReleasePolicy{When: "after a feature, at most once a day", Check: "sh check.sh {version}", Approve: approve}
	if remoteNamed && !pr {
		policy.GitHub = "owner/repo"
	}
	p, err = a.SetRelease(ctx, p.ID, policy)
	if err != nil {
		t.Fatal(err)
	}
	a.githubURL = func(string) string { return remote }
	return a, p, runner, source, remote, tip
}
func proposeFromPM(t *testing.T, a *Loop, p core.Project) {
	t.Helper()
	if err := a.pmTurn(context.Background(), p.ID, core.Role{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
}
func releasePass(t *testing.T, a *Loop) int {
	t.Helper()
	ctx := context.Background()
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := a.manageReleases(ctx, snap, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range jobs {
		if panic := <-ch; panic != nil {
			t.Fatalf("release panic: %v", panic)
		}
	}
	return len(jobs)
}
func releaseState(t *testing.T, a *Loop, id string) (core.Project, core.Snapshot) {
	t.Helper()
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, ok := findProject(snap, id)
	if !ok {
		t.Fatal("missing project")
	}
	return p, snap
}
func chooseRelease(t *testing.T, a *Loop, id, choice string) {
	t.Helper()
	p, _ := releaseState(t, a, id)
	if p.Release == nil || p.Release.DecisionID == "" {
		t.Fatal("no decision", p)
	}
	if _, err := a.Core.ChooseDecision(context.Background(), p.Release.DecisionID, choice, core.FromOwner); err != nil {
		t.Fatal(err)
	}
}
func tagPresent(t *testing.T, dir, tag string) bool {
	t.Helper()
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/tags/"+tag)
	cmd.Dir = dir
	return cmd.Run() == nil
}
func TestReleaseOwnerApprovalChecksAndPublishes(t *testing.T) {
	t.Parallel()
	a, p, r, source, remote, tip := releaseFixture(t, "", false, true, 0)
	proposeFromPM(t, a, p)
	pending, snap := releaseState(t, a, p.ID)
	if pending.Release == nil || pending.Release.State != "proposed" || !strings.Contains(snap.Decisions[len(snap.Decisions)-1].Context, "User-visible feature") {
		t.Fatal(pending, snap.Decisions)
	}
	if releasePass(t, a) != 0 || r.checks != 0 {
		t.Fatal("ran before approval")
	}
	chooseRelease(t, a, p.ID, "Release v1.1.0")
	releasePass(t, a)
	releasePass(t, a)
	done, snap := releaseState(t, a, p.ID)
	if done.Release != nil || len(done.Releases) != 1 || done.Releases[0].Commit != tip || done.Releases[0].Published != "owner/repo" || done.Releases[0].ApprovedBy != "owner" || done.Releases[0].At.IsZero() {
		t.Fatal(done)
	}
	for _, dir := range []string{source, remote} {
		if ownerGit(t, dir, "rev-parse", "v1.1.0^{commit}") != tip || ownerGit(t, dir, "cat-file", "-t", "v1.1.0") != "tag" {
			t.Fatal("wrong tag")
		}
		if !strings.Contains(ownerGit(t, dir, "cat-file", "-p", "v1.1.0"), "Adds a user-visible feature.") {
			t.Fatal("notes missing")
		}
	}
	if ownerGit(t, remote, "rev-parse", "main") != tip || r.checks != 1 {
		t.Fatal("branch/check", r.checks)
	}
	spec := r.specs[len(r.specs)-1]
	if !spec.Write || spec.Web || !strings.Contains(spec.Prompt, "sh check.sh v1.1.0") || !strings.Contains(spec.Prompt, tip) || spec.WorkDir == spec.Read[len(spec.Read)-1] {
		t.Fatal(spec)
	}
	info, err := os.Stat(source)
	if err != nil || info == nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range snap.Activity {
		if e.Kind == "release.checked" && strings.Contains(e.Summary, "exit 0") && strings.Contains(e.Summary, "tail-evidence") {
			found = true
		}
	}
	if !found {
		t.Fatal("check activity missing")
	}
}
func TestReleasePMApprovalPRPushesOnlyTag(t *testing.T) {
	t.Parallel()
	a, p, r, _, remote, tip := releaseFixture(t, core.ApprovePM, true, false, 0)
	proposeFromPM(t, a, p)
	before := ownerGit(t, remote, "rev-parse", "main")
	releasePass(t, a)
	releasePass(t, a)
	done, snap := releaseState(t, a, p.ID)
	if len(done.Releases) != 1 || done.Releases[0].ApprovedBy != "pm" || done.Releases[0].Published != "owner/repo" || r.checks != 1 {
		t.Fatal(done)
	}
	for _, d := range snap.Decisions {
		if d.Kind == core.DecisionRelease {
			t.Fatal("PM release asked owner")
		}
	}
	if ownerGit(t, remote, "rev-parse", "main") != before || ownerGit(t, remote, "rev-parse", "v1.1.0^{commit}") != tip {
		t.Fatal("PR branch moved")
	}
}
func TestReleaseLocalOnlyAndDeclined(t *testing.T) {
	t.Parallel()
	a, p, _, source, remote, _ := releaseFixture(t, "", false, false, 0)
	proposeFromPM(t, a, p)
	chooseRelease(t, a, p.ID, "Not now")
	if releasePass(t, a) != 0 || tagPresent(t, source, "v1.1.0") {
		t.Fatal("declined release ran")
	}
	proposeFromPM(t, a, p)
	chooseRelease(t, a, p.ID, "Release v1.1.0")
	releasePass(t, a)
	releasePass(t, a)
	done, _ := releaseState(t, a, p.ID)
	if len(done.Releases) != 1 || done.Releases[0].Published != "" || done.Releases[0].Note != "tag not published" || !tagPresent(t, source, "v1.1.0") || tagPresent(t, remote, "v1.1.0") {
		t.Fatal(done)
	}
}
func TestReleaseFailedCheckNeverRetriesAutomatically(t *testing.T) {
	t.Parallel()
	a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 3)
	proposeFromPM(t, a, p)
	releasePass(t, a)
	failed, snap := releaseState(t, a, p.ID)
	if failed.Release == nil || failed.Release.DecisionID == "" || len(failed.Releases) != 0 || tagPresent(t, source, "v1.1.0") {
		t.Fatal(failed)
	}
	if !strings.Contains(snap.Decisions[len(snap.Decisions)-1].Context, "exit 3") || !strings.Contains(snap.Decisions[len(snap.Decisions)-1].Context, "tail-evidence") {
		t.Fatal(snap.Decisions)
	}
	for range 3 {
		releasePass(t, a)
	}
	if r.checks != 1 {
		t.Fatal("automatic retry", r.checks)
	}
	chooseRelease(t, a, p.ID, "Try again")
	releasePass(t, a)
	if r.checks != 2 {
		t.Fatal("explicit retry missing")
	}
}
func TestOwnerReleaseKeepsProposalContentsBeforeApproval(t *testing.T) {
	t.Parallel()
	a, p, r, source, remote, tip := releaseFixture(t, "", false, true, 0)
	proposeFromPM(t, a, p)
	pending, snap := releaseState(t, a, p.ID)
	if pending.Release.Commit != tip || !pending.Release.Started.IsZero() {
		t.Fatal("proposal must pin contents without starting", pending.Release)
	}
	ownerGit(t, source, "commit", "--allow-empty", "-qm", "Not included in approval")
	for _, d := range snap.Decisions {
		if d.ID == pending.Release.DecisionID && strings.Contains(d.Context, "Not included in approval") {
			t.Fatal("decision unexpectedly includes later landing", d)
		}
	}
	chooseRelease(t, a, p.ID, "Release v1.1.0")
	releasePass(t, a)
	checked, _ := releaseState(t, a, p.ID)
	if checked.Release.Started.IsZero() || checked.Release.Commit != tip || r.checks != 1 {
		t.Fatal("check did not keep proposal commit", checked.Release)
	}
	releasePass(t, a)
	done, _ := releaseState(t, a, p.ID)
	if len(done.Releases) != 1 || done.Releases[0].Commit != tip || ownerGit(t, source, "rev-parse", "v1.1.0^{commit}") != tip || ownerGit(t, remote, "rev-parse", "main") != tip {
		t.Fatal("released changes absent from approval", done.Releases)
	}
}

func TestReleasePauseAndMovingTarget(t *testing.T) {
	t.Parallel()
	a, p, r, source, remote, tip := releaseFixture(t, core.ApprovePM, false, true, 0)
	ctx := context.Background()
	proposeFromPM(t, a, p)
	if _, err := a.Core.SetLandingPaused(ctx, p.ID, true, "freeze"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if releasePass(t, a) != 0 {
			t.Fatal("started while paused")
		}
	}
	if r.checks != 0 {
		t.Fatal("checked while paused")
	}
	a.Core.SetLandingPaused(ctx, p.ID, false, "")
	r.before = func(spec roles.Spec) {
		// A later landing cannot change the checked or published commit.
		ownerGit(t, source, "commit", "--allow-empty", "-qm", "Later landing")
		a.Core.SetLandingPaused(ctx, p.ID, true, "freeze during check")
		info, err := os.Stat(spec.Read[len(spec.Read)-1])
		if err != nil || info.Mode().Perm()&0222 != 0 {
			t.Fatal("checkout writable", err)
		}
	}
	releasePass(t, a)
	for range 3 {
		if releasePass(t, a) != 0 {
			t.Fatal("published during pause")
		}
	}
	if tagPresent(t, source, "v1.1.0") {
		t.Fatal("tagged while paused")
	}
	a.Core.SetLandingPaused(ctx, p.ID, false, "")
	releasePass(t, a)
	if ownerGit(t, source, "rev-parse", "v1.1.0^{commit}") != tip || ownerGit(t, remote, "rev-parse", "main") != tip {
		t.Fatal("unchecked landing published")
	}
}
func TestReleaseDivergenceLeavesLocalTag(t *testing.T) {
	t.Parallel()
	for _, retry := range []bool{false, true} {
		t.Run(fmt.Sprint(retry), func(t *testing.T) {
			t.Parallel()
			a, p, _, source, remote, tip := releaseFixture(t, core.ApprovePM, false, true, 0)
			foreign := ownerGit(t, source, "commit-tree", "v1.0.0^{tree}", "-p", "v1.0.0", "-m", "foreign")
			ownerGit(t, remote, "fetch", source, foreign)
			ownerGit(t, remote, "update-ref", "refs/heads/main", foreign)
			proposeFromPM(t, a, p)
			releasePass(t, a)
			releasePass(t, a)
			failed, snap := releaseState(t, a, p.ID)
			if !tagPresent(t, source, "v1.1.0") || tagPresent(t, remote, "v1.1.0") || ownerGit(t, remote, "rev-parse", "main") != foreign || failed.Release.DecisionID == "" || !strings.Contains(snap.Decisions[len(snap.Decisions)-1].Context, "nothing was pushed") {
				t.Fatal(failed)
			}
			if retry {
				ownerGit(t, remote, "update-ref", "refs/heads/main", ownerGit(t, source, "rev-parse", "v1.0.0"))
				chooseRelease(t, a, p.ID, "Try again")
				releasePass(t, a)
				if ownerGit(t, remote, "rev-parse", "v1.1.0^{commit}") != tip {
					t.Fatal("not retried")
				}
			} else {
				chooseRelease(t, a, p.ID, "Leave it")
			}
			done, _ := releaseState(t, a, p.ID)
			if done.Release != nil || len(done.Releases) != 1 || (retry && done.Releases[0].Published == "") || (!retry && done.Releases[0].Published != "") {
				t.Fatal(done)
			}
		})
	}
}
func TestReleaseNoSettingsAndSettingsRemoval(t *testing.T) {
	t.Parallel()
	a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 0)
	if _, err := a.SetRelease(context.Background(), p.ID, nil); err != nil {
		t.Fatal(err)
	}
	proposeFromPM(t, a, p)
	done, _ := releaseState(t, a, p.ID)
	if done.Release != nil || releasePass(t, a) != 0 || r.checks != 0 || tagPresent(t, source, "v1.1.0") {
		t.Fatal("unconfigured project released")
	}
	if strings.Contains(r.specs[0].Prompt, "When to release:") {
		t.Fatal("unconfigured release prompt")
	}
	a.SetRelease(context.Background(), p.ID, &core.ReleasePolicy{When: "after a feature", Approve: core.ApprovePM})
	proposeFromPM(t, a, p)
	a.SetRelease(context.Background(), p.ID, &core.ReleasePolicy{When: "after a feature"})
	pending, _ := releaseState(t, a, p.ID)
	if pending.Release.State != "proposed" || pending.Release.DecisionID == "" {
		t.Fatal("approval switch ignored")
	}
	a.SetRelease(context.Background(), p.ID, nil)
	done, snap := releaseState(t, a, p.ID)
	if done.Release != nil || snap.Decisions[len(snap.Decisions)-1].Status != core.DecisionDismissed {
		t.Fatal("removal failed")
	}
}
func TestReleaseReconcilesPublishingAndChecking(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"checking", "branch pushed", "tag pushed", "version taken"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			a, p, r, source, remote, tip := releaseFixture(t, core.ApprovePM, false, true, 0)
			ctx := context.Background()
			proposeFromPM(t, a, p)
			c, _, ok, err := a.Core.ClaimRelease(ctx, p.ID, tip, func(core.Role) string { return "" })
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			a.Core.ReleaseProjectClaim(ctx, p.ID, c.Token)
			if stage == "checking" {
				releasePass(t, a)
				releasePass(t, a)
			} else {
				a.Core.ReleaseChecked(ctx, p.ID, true, 0, "passed before restart")
				c, _, ok, err = a.Core.ClaimRelease(ctx, p.ID, tip, func(core.Role) string { return "" })
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				a.Core.ReleaseProjectClaim(ctx, p.ID, c.Token)
				m, err := a.gitMediumFor(ctx, p, p.Playbook)
				if err != nil {
					t.Fatal(err)
				}
				if err = m.repo.MakeTag(ctx, tip, "v1.1.0", "Adds a user-visible feature."); err != nil {
					t.Fatal(err)
				}
				m.repo.TagSource(ctx, "v1.1.0")
				if stage == "version taken" {
					ownerGit(t, remote, "tag", "v1.1.0", "v1.0.0")
				} else {
					if err = m.repo.PushReleaseBranch(ctx, remote, tip, "main", nil); err != nil {
						t.Fatal(err)
					}
					if stage == "tag pushed" {
						if err = m.repo.PushTag(ctx, remote, "v1.1.0", nil); err != nil {
							t.Fatal(err)
						}
					}
				}
				releasePass(t, a)
			}
			done, _ := releaseState(t, a, p.ID)
			if stage == "version taken" {
				if done.Release == nil || done.Release.DecisionID == "" || len(done.Releases) != 0 {
					t.Fatal(done)
				}
				if ownerGit(t, remote, "rev-parse", "main") == tip {
					t.Fatal("branch pushed despite a conflicting tag")
				}
			} else {
				if done.Release != nil || len(done.Releases) != 1 || !tagPresent(t, source, "v1.1.0") || !tagPresent(t, remote, "v1.1.0") {
					t.Fatal(done)
				}
				for range 3 {
					releasePass(t, a)
				}
				done, _ = releaseState(t, a, p.ID)
				if len(done.Releases) != 1 {
					t.Fatal("recorded twice")
				}
			}
			if (stage == "checking" && r.checks != 1) || (stage != "checking" && r.checks != 0) {
				t.Fatal("wrong checks", r.checks)
			}
		})
	}
}

func TestReleaseCheckChangesAndMissingQARequireOwner(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"changed checkout", "missing QA"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 0)
			proposeFromPM(t, a, p)
			if mode == "missing QA" {
				_, err := a.Core.EditPlaybook(context.Background(), p.ID, func(_ *core.Snapshot, _ *core.Project, pb *core.Playbook) error {
					roles := pb.Roles[:0:0]
					for _, seat := range pb.Roles {
						if !seat.Holds(core.RoleQA) {
							roles = append(roles, seat)
						}
					}
					pb.Roles = roles
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				r.before = func(spec roles.Spec) {
					dir := spec.Read[len(spec.Read)-1]
					if err := os.Chmod(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "unexpected.txt"), []byte("changed"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			releasePass(t, a)
			failed, snap := releaseState(t, a, p.ID)
			if failed.Release == nil || failed.Release.DecisionID == "" || len(failed.Releases) != 0 || tagPresent(t, source, "v1.1.0") {
				t.Fatal(failed)
			}
			reason := "changed the revision"
			if mode == "missing QA" {
				reason = "no QA"
			}
			found := false
			for _, d := range snap.Decisions {
				if d.ID == failed.Release.DecisionID && strings.Contains(d.Context, reason) {
					found = true
				}
			}
			if !found {
				t.Fatal(snap.Decisions)
			}
			for range 3 {
				releasePass(t, a)
			}
			if mode == "missing QA" && r.checks != 0 {
				t.Fatal("ran missing QA")
			}
			if mode == "changed checkout" && r.checks != 1 {
				t.Fatal("retried changed checkout")
			}
		})
	}
}

func TestReleaseSettingsRemainOwnerOnlyAndStartedCheckFinishes(t *testing.T) {
	t.Parallel()
	a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 0)
	for _, d := range a.managerTools(p.ID, core.Role{Name: "Pim", Kinds: []string{core.RolePM}}).proposing(p.Playbook).Definitions() {
		if strings.Contains(d.Name, "release") {
			t.Fatal("PM release settings tool", d.Name)
		}
	}
	if _, err := a.SetTeam(context.Background(), p.ID, TeamChoice{Template: "code", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	current, _ := releaseState(t, a, p.ID)
	if current.Playbook.Release == nil {
		t.Fatal("team choice removed release settings")
	}
	proposeFromPM(t, a, p)
	r.before = func(roles.Spec) {
		if _, err := a.SetRelease(context.Background(), p.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	releasePass(t, a)
	releasePass(t, a)
	done, _ := releaseState(t, a, p.ID)
	if done.Playbook.Release != nil || len(done.Releases) != 1 || !tagPresent(t, source, "v1.1.0") {
		t.Fatal("started release dropped", done)
	}
}

func TestReleaseTailIsBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	tail := releaseTail(strings.Repeat("line\n", 70) + "Authorization: Bearer sample-secret\nghp_sampletoken\ngithub_pat_sampletoken")
	if len(strings.Split(tail, "\n")) > 40 || strings.Contains(tail, "sample-secret") || strings.Contains(tail, "sampletoken") {
		t.Fatal(tail)
	}
	if len(releaseTail(strings.Repeat("x", 9000))) > 4096 {
		t.Fatal("unbounded tail")
	}
}

func TestReleaseTagPushFailureRetriesOnlyPublication(t *testing.T) {
	t.Parallel()
	a, p, r, source, remote, tip := releaseFixture(t, core.ApprovePM, false, true, 0)
	proposeFromPM(t, a, p)
	releasePass(t, a)
	ownerGit(t, remote, "config", "receive.hideRefs", "refs/tags")
	releasePass(t, a)
	failed, _ := releaseState(t, a, p.ID)
	if failed.Release == nil || failed.Release.State != "publishing" || failed.Release.DecisionID == "" || !tagPresent(t, source, "v1.1.0") || tagPresent(t, remote, "v1.1.0") || ownerGit(t, remote, "rev-parse", "main") != tip {
		t.Fatal(failed)
	}
	for range 3 {
		releasePass(t, a)
	}
	if r.checks != 1 {
		t.Fatal("retried check")
	}
	ownerGit(t, remote, "config", "--unset", "receive.hideRefs")
	chooseRelease(t, a, p.ID, "Try again")
	releasePass(t, a)
	done, _ := releaseState(t, a, p.ID)
	if done.Release != nil || len(done.Releases) != 1 || !tagPresent(t, remote, "v1.1.0") || r.checks != 1 {
		t.Fatal(done)
	}
}

func TestReleaseWithNoCheckPublishesWithoutQA(t *testing.T) {
	t.Parallel()
	a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 0)
	if _, err := a.SetRelease(context.Background(), p.ID, &core.ReleasePolicy{When: "after features", Approve: core.ApprovePM}); err != nil {
		t.Fatal(err)
	}
	proposeFromPM(t, a, p)
	releasePass(t, a)
	done, _ := releaseState(t, a, p.ID)
	if len(done.Releases) != 1 || r.checks != 0 || !tagPresent(t, source, "v1.1.0") {
		t.Fatal(done)
	}
}

func TestReleaseFailedVerdictPreservesCommandExitStatus(t *testing.T) {
	t.Parallel()
	a, p, r, source, _, _ := releaseFixture(t, core.ApprovePM, false, false, 0)
	r.verdict = `{"outcome":"fail","exit_status":0,"output":"verification failed"}`
	proposeFromPM(t, a, p)
	releasePass(t, a)
	failed, snap := releaseState(t, a, p.ID)
	if failed.Release == nil || failed.Release.DecisionID == "" || tagPresent(t, source, "v1.1.0") {
		t.Fatal(failed)
	}
	found := false
	for _, d := range snap.Decisions {
		if d.ID == failed.Release.DecisionID && strings.Contains(d.Context, "exit 0") && strings.Contains(d.Context, "QA reported fail") {
			found = true
		}
	}
	if !found {
		t.Fatal("exit status lost", snap.Decisions)
	}
}

func TestReleaseAbandonedPRTagDoesNotBlockNextProposal(t *testing.T) {
	t.Parallel()
	a, p, _, source, remote, _ := releaseFixture(t, "pm", true, false, 0)
	proposeFromPM(t, a, p)
	releasePass(t, a)
	// Fail only tag publication after QA, leaving the annotated tag in the clone.
	moved := remote + ".saved"
	if err := os.Rename(remote, moved); err != nil {
		t.Fatal(err)
	}
	releasePass(t, a)
	saved, _ := releaseState(t, a, p.ID)
	if saved.Release == nil || saved.Release.DecisionID == "" || saved.Release.LocalTag {
		t.Fatal(saved.Release)
	}
	chooseRelease(t, a, p.ID, "Leave it")
	if err := os.Rename(moved, remote); err != nil {
		t.Fatal(err)
	}
	ownerGit(t, source, "commit", "--allow-empty", "-qm", "Another feature")
	newTip := ownerGit(t, source, "rev-parse", "HEAD")
	ownerGit(t, source, "push", remote, "main")
	saved, _ = releaseState(t, a, p.ID)
	proposeFromPM(t, a, saved)
	releasePass(t, a)
	releasePass(t, a)
	saved, _ = releaseState(t, a, p.ID)
	if saved.Release != nil || len(saved.Releases) != 1 || saved.Releases[0].Commit != newTip {
		t.Fatal(saved.Release, saved.Releases)
	}
}
func TestReleasePromptUsesClockAndLatestDeclineOnly(t *testing.T) {
	t.Parallel()
	a, p, r, _, _, _ := releaseFixture(t, "", false, false, 0)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	a.Now = func() time.Time { return now }
	for range 3 {
		saved, _ := releaseState(t, a, p.ID)
		proposeFromPM(t, a, saved)
		chooseRelease(t, a, p.ID, "Not now")
	}
	saved, snap := releaseState(t, a, p.ID)
	declinedAt := snap.Decisions[len(snap.Decisions)-1].ResolvedAt.UTC().Format(time.RFC3339)
	proposeFromPM(t, a, saved)
	prompt := r.specs[len(r.specs)-1].Prompt
	if strings.Count(prompt, "Previously declined:") != 1 || !strings.Contains(prompt, "Current time: "+now.Format(time.RFC3339)) || !strings.Contains(prompt, "Previously declined: Release v1.1.0 at "+declinedAt) {
		t.Fatal(prompt)
	}
}
func TestReleaseActiveCheckDoesNotReportWaitingOnItself(t *testing.T) {
	t.Parallel()
	a, p, r, _, _, _ := releaseFixture(t, "pm", false, false, 0)
	proposeFromPM(t, a, p)
	r.before = func(spec roles.Spec) {
		if spec.Observer == nil {
			t.Fatal("QA has no live observer")
		}
		spec.Observer.Started()
		defer spec.Observer.Ended()
		turns := a.Turns()
		if len(turns) != 1 || turns[0].ProjectID != p.ID || turns[0].Role != core.RoleQA {
			t.Fatal(turns)
		}
		snap, _ := a.Core.Snapshot(context.Background())
		jobs, err := a.manageReleases(context.Background(), snap, true)
		if err != nil || len(jobs) != 0 {
			t.Fatal(jobs, err)
		}
		_, snap = releaseState(t, a, p.ID)
		for _, item := range snap.Activity {
			if item.Kind == "release.waiting" {
				t.Fatal(item)
			}
		}
	}
	releasePass(t, a)
}

func TestReleaseWaitsForSeatBeforeFetchingTarget(t *testing.T) {
	t.Parallel()
	a, p, runner, _, remote, tip := releaseFixture(t, "pm", true, false, 0)
	proposeFromPM(t, a, p)
	a.gate.interactive = 1
	moved := remote + ".saved"
	if err := os.Rename(remote, moved); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if releasePass(t, a) != 0 {
			t.Fatal("started without a seat")
		}
	}
	saved, _ := releaseState(t, a, p.ID)
	if saved.Release == nil || saved.Release.DecisionID != "" || saved.Release.Commit != tip || !saved.Release.Started.IsZero() || runner.checks != 0 {
		t.Fatal("fetched unavailable repository while waiting", saved.Release)
	}
	if err := os.Rename(moved, remote); err != nil {
		t.Fatal(err)
	}
	a.gate.interactive = 0
	if releasePass(t, a) != 1 || runner.checks != 1 {
		t.Fatal("did not resume")
	}
}
func TestReleaseSchedulingErrorDoesNotStopOtherProjects(t *testing.T) {
	t.Parallel()
	a, p, runner, _, _, _ := releaseFixture(t, "pm", false, false, 0)
	proposeFromPM(t, a, p)
	snap, _ := a.Core.Snapshot(context.Background())
	saved, _ := findProject(snap, p.ID)
	missing := saved
	missing.ID = "removed-project"
	snap.Projects = append([]core.Project{missing}, snap.Projects...)
	jobs, err := a.manageReleases(context.Background(), snap, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if panic := <-job; panic != nil {
			t.Fatal(panic)
		}
	}
	if len(jobs) != 1 || runner.checks != 1 {
		t.Fatal("other project was skipped", len(jobs), runner.checks)
	}
}

func TestReleasePMSeesRemoteOffTargetVersionOnPushProject(t *testing.T) {
	t.Parallel()
	a, p, _, source, remote, tip := releaseFixture(t, "pm", false, true, 0)
	ownerGit(t, source, "checkout", "-qb", "off-target")
	ownerGit(t, source, "commit", "--allow-empty", "-qm", "off target")
	ownerGit(t, source, "tag", "v2.0.0")
	ownerGit(t, source, "push", remote, "refs/tags/v2.0.0")
	ownerGit(t, source, "tag", "-d", "v2.0.0")
	ownerGit(t, source, "checkout", "main")
	seen, err := a.releaseContext(context.Background(), p)
	if err != nil || seen.Highest != "v2.0.0" || seen.Latest != "v1.0.0" || seen.Count != 1 {
		t.Fatal(seen, err)
	}
	if ownerGit(t, source, "rev-parse", "main") != tip {
		t.Fatal("owner branch changed")
	}
	proposeFromPM(t, a, p)
	saved, _ := releaseState(t, a, p.ID)
	if saved.Release != nil {
		t.Fatal("occupied version proposed", saved.Release)
	}
}
