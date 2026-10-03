package work

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

func (lp *Loop) SetRelease(ctx context.Context, id string, policy *core.ReleasePolicy) (core.Project, error) {
	return lp.editPlaybook(ctx, id, "releases are for code projects", func(v *core.Snapshot, p *core.Project, pb *core.Playbook) error {
		pb.Release = nil
		if policy != nil {
			trimmed := policy.Trimmed()
			pb.Release = &trimmed
		}
		core.UpdateReleaseSettings(v, p, pb.Release)
		p.PMDue = true
		return nil
	})
}
func (lp *Loop) releaseContext(ctx context.Context, p core.Project) (core.ReleaseContext, error) {
	m, err := lp.gitMediumFor(ctx, p, p.Playbook)
	if err != nil {
		return core.ReleaseContext{}, err
	}
	defer m.locked()()
	tip, err := releaseTarget(ctx, m, p.Playbook.Land)
	if err != nil {
		return core.ReleaseContext{}, err
	}
	from := ""
	var config []string
	if p.Playbook.Land.PullRequests {
		from = m.url()
		config = github.CredentialConfig()
	}
	if err = m.repo.FetchVersionTags(ctx, from, config); err != nil {
		return core.ReleaseContext{}, err
	}
	latest, err := m.repo.LatestVersionTag(ctx, tip)
	if err != nil {
		return core.ReleaseContext{}, err
	}
	highest, err := m.repo.LatestVersionTag(ctx, "")
	if err != nil {
		return core.ReleaseContext{}, err
	}
	if !p.Playbook.Land.PullRequests && p.Playbook.Release.GitHub != "" {
		remoteHighest, remoteErr := m.repo.HighestRemoteVersion(ctx, m.remote(p.Playbook.Release.GitHub), github.CredentialConfig())
		if remoteErr != nil {
			return core.ReleaseContext{}, remoteErr
		}
		if remoteHighest != "" && (highest == "" || core.CompareVersions(remoteHighest, highest) > 0) {
			highest = remoteHighest
		}
	}
	commits, count, err := m.repo.CommitsSince(ctx, latest, tip)
	return core.ReleaseContext{Commit: tip, Latest: latest, Highest: highest, Commits: commits, Count: count}, err
}
func releaseTarget(ctx context.Context, m gitMedium, land core.LandPolicy) (string, error) {
	if land.PullRequests {
		return m.repo.FetchFrom(ctx, m.remote(land.GitHub), land.Target, github.CredentialConfig())
	}
	return m.repo.Fetch(ctx, land.Target)
}
func pmReleasePrompt(p core.Project, seen core.ReleaseContext, now time.Time) string {
	if p.Playbook == nil || p.Playbook.Release == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nReleases\nWhen to release: %s\nCurrent time: %s\nLatest version tag: %s\n", p.Playbook.Release.When, now.UTC().Format(time.RFC3339), seen.Latest)
	fmt.Fprintf(&b, "Highest occupied version (including tags outside the target): %s; choose above it.\n", seen.Highest)
	if seen.Error != "" {
		fmt.Fprintf(&b, "Cannot read landed commits: %s. Do not propose a release.\n", seen.Error)
	} else {
		fmt.Fprintf(&b, "Landed since that tag (%d commits; newest 50 shown):\n%s\n", seen.Count, strings.Join(seen.Commits, "\n"))
	}
	if len(p.Releases) > 0 {
		r := p.Releases[0]
		fmt.Fprintf(&b, "Last recorded release: %s at %s: %s\n", r.Version, r.At.Format(time.RFC3339), r.Notes)
	}
	if p.Release != nil {
		fmt.Fprintf(&b, "Pending release: %s (%s). Do not propose another.\n", p.Release.Version, p.Release.State)
	}
	b.WriteString("You may include an optional JSON field \"release\": {\"version\": \"next strict semver version\", \"notes\": \"short release notes, at most 2000 characters\"}. Choose the bump from the landed changes and the guidance. Omit release when it is not time. Only the owner changes release settings.\n")
	return b.String()
}

// manageReleases uses ordinary fenced project jobs, including the QA seat.
// A failed run has a decision, so subsequent passes cannot retry it.
func (lp *Loop) manageReleases(ctx context.Context, snap core.Snapshot, waited bool) ([]<-chan any, error) {
	var started []<-chan any
	for _, p := range snap.Projects {
		r := p.Release
		if r == nil || r.DecisionID != "" || r.State == "proposed" {
			continue
		}
		running := false
		for _, c := range p.Claims {
			if c.Step == core.StepReleaseCheck || c.Step == core.StepReleasePublish {
				running = true
			}
		}
		if running {
			continue
		}
		if core.ReleaseHeld(snap, p) != "" {
			if err := lp.Core.WaitRelease(ctx, p.ID); err != nil {
				lp.releaseSchedulingError(p.ID, err)
				continue
			}
			continue
		}
		pb := *p.Playbook
		pb.Land = r.Land
		m, err := lp.gitMediumFor(ctx, p, &pb)
		if err != nil {
			if err = lp.Core.ReleaseFailed(ctx, p.ID, releaseTail(err.Error()), r.LocalTag); err != nil {
				lp.releaseSchedulingError(p.ID, err)
				continue
			}
			continue
		}
		taken := &slots{lp: lp}
		usage := map[string]string{}
		if r.State != "publishing" && r.Policy.Check != "" {
			for _, qa := range pb.Roles {
				if qa.Holds(core.RoleQA) {
					if wait, _ := lp.usageWait(ctx, qa); !wait.IsZero() {
						usage[qa.Name] = "waiting for usage allowance"
					}
				}
			}
		}
		admit := func(seat core.Role) string {
			if why := usage[seat.Name]; why != "" {
				return why
			}
			return taken.admit(seat)
		}
		c, seat, ok, err := lp.Core.ClaimRelease(ctx, p.ID, r.Commit, admit)
		if err != nil {
			taken.giveBack()
			lp.releaseSchedulingError(p.ID, err)
			continue
		}
		if !ok {
			taken.giveBack()
			continue
		}
		id, token := p.ID, c.Token
		started = append(started, lp.run(ctx, claimed{project: id, token: token, seat: seat}, waited,
			func(ctx context.Context) error {
				return lp.releaseTurn(core.FencedProject(ctx, id, token), id, m, seat, c.Step)
			},
			func(ctx context.Context) error { return lp.Core.ReleaseProjectClaim(ctx, id, token) }))
	}
	return started, nil
}
func (lp *Loop) releaseTurn(ctx context.Context, id string, m gitMedium, seat core.Role, step string) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	p, ok := findProject(snap, id)
	if !ok || p.Release == nil {
		return core.ErrConflict
	}
	r := p.Release
	if r.Commit == "" {
		unlock := m.locked()
		commit, targetErr := releaseTarget(ctx, m, r.Land)
		unlock()
		if targetErr != nil {
			return lp.Core.ReleaseFailed(ctx, id, releaseTail(targetErr.Error()), r.LocalTag)
		}
		if err := lp.Core.PinRelease(ctx, id, commit); err != nil {
			return err
		}
		r.Commit = commit
	}
	if step == core.StepReleaseCheck {
		return lp.checkRelease(ctx, p, m, seat)
	}
	defer m.locked()()
	local := r.LocalTag
	fail := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return lp.Core.ReleaseFailed(ctx, id, fmt.Sprintf("Publication exit %d\n%s", gitrepo.ExitStatus(err), releaseTail(err.Error())), local)
	}
	destination := r.Land.GitHub
	if !r.Land.PullRequests {
		destination = r.Policy.GitHub
	}
	url := ""
	if destination != "" {
		url = m.remote(destination)
	}
	if err = m.repo.DiscardUnpublishedTag(ctx, r.Version, r.Commit, url, github.CredentialConfig()); err != nil {
		return fail(err)
	}
	if err = m.repo.MakeTag(ctx, r.Commit, r.Version, r.Notes); err != nil {
		return fail(err)
	}
	published := r.Land.GitHub
	if !r.Land.PullRequests {
		if err = m.repo.TagSource(ctx, r.Version); err != nil {
			return fail(err)
		}
		local = true
		published = r.Policy.GitHub
		if published != "" {
			if err = m.repo.RecoverReleaseTag(ctx, m.remote(published), r.Version, r.Commit, github.CredentialConfig()); err != nil {
				return fail(err)
			}
			if err = m.repo.PushReleaseBranch(ctx, m.remote(published), r.Commit, r.Land.Target, github.CredentialConfig()); err != nil {
				if errors.Is(err, gitrepo.ErrTargetMoved) {
					err = fmt.Errorf("GitHub's branch has commits the owner's repository doesn't have; nothing was pushed to GitHub: %w", err)
				}
				return fail(err)
			}
		}
	}
	if published != "" {
		if err = m.repo.PushTag(ctx, m.remote(published), r.Version, github.CredentialConfig()); err != nil {
			return fail(err)
		}
	}
	note := ""
	if published == "" {
		note = "tag not published"
	}
	return lp.Core.FinishRelease(ctx, id, published, note)
}

var releaseSecrets = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|(?i:authorization:[^\r\n]+)|https?://[^/\s]*:[^@\s]+@`)

func releaseTail(s string) string {
	s = releaseSecrets.ReplaceAllString(s, "[redacted]")
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	s = strings.Join(lines, "\n")
	if len(s) > 4096 {
		s = s[len(s)-4096:]
	}
	return s
}

// checkRelease shares QA's checkout, sandbox spec, JSON retry and verification.
// It gives QA no publishing tools or credentials.
func (lp *Loop) checkRelease(ctx context.Context, p core.Project, m gitMedium, seat core.Role) error {
	r := p.Release
	c, err := m.check(ctx, core.Task{}, core.Revision{Ref: r.Commit}, true, false)
	if err != nil {
		return lp.Core.ReleaseFailed(ctx, p.ID, err.Error(), false)
	}
	defer c.remove()
	command := strings.ReplaceAll(r.Policy.Check, "{version}", r.Version)
	prompt := fmt.Sprintf("Check release %s on landed commit %s. This command verifies only; publish nothing. Run exactly this from the repository root, once:\n%s\n%s\nReply only JSON: {\"outcome\":\"pass or fail\",\"exit_status\":0,\"output\":\"last 40 lines or 4 KB of output\"}. A nonzero exit must fail.\n", r.Version, r.Commit, command, c.note)
	spec := lp.baseSpec(seat, c.workDir, prompt)
	spec.Observer = lp.watchTurn(core.Task{ProjectID: p.ID}, core.RoleQA, seat, c.workDir, false)
	// A release has no task; the globally unique project ID scopes its temporary
	// learnings folder, shared only by this project turn and member.
	learned, err := lp.prepareLearnings(core.Task{ID: p.ID}, seat)
	if err != nil {
		return lp.Core.ReleaseFailed(ctx, p.ID, err.Error(), false)
	}
	defer learned.cleanup()
	if learned.index != "" {
		spec.Read = append(spec.Read, learned.dir)
		spec.Instructions += "\n\n" + learned.index
	}
	spec.Write = true
	spec.Env = c.env
	spec.Read = append(append(spec.Read, m.readable()...), c.read...)
	spec.Loopback = checkLoopback(&m.playbook, seat)
	type checkResult struct {
		Outcome string `json:"outcome"`
		Status  *int   `json:"exit_status"`
		Output  string `json:"output"`
	}
	var result checkResult
	reply, _, parseErr, runErr := lp.askForJSON(ctx, spec, func(reply string) error {
		result = checkResult{}
		if err := decodeReply(reply, &result); err != nil {
			return err
		}
		if result.Status == nil || (result.Outcome != "pass" && result.Outcome != "fail") {
			return errors.New("give outcome, exit_status and output")
		}
		return nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	status, tail := -1, result.Output
	if result.Status != nil {
		status = *result.Status
	}
	passed := result.Outcome == "pass" && status == 0
	if result.Outcome == "fail" {
		tail = "QA reported fail\n" + tail
	}
	if runErr != nil {
		passed = false
		status, tail = -1, runErr.Error()
	} else if parseErr != nil {
		passed = false
		status, tail = -1, parseErr.Error()+"\n"+reply
	} else if err = c.verify(ctx); err != nil {
		passed = false
		tail = err.Error() + "\n" + tail
	}
	return lp.Core.ReleaseChecked(ctx, p.ID, passed, status, releaseTail(tail))
}

func (lp *Loop) releaseSchedulingError(id string, err error) {
	if lp.Diagnostics != nil {
		lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "release_schedule", ProjectID: id}, err)
	}
}
