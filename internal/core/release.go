package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/releaseversion"
)

const (
	DecisionRelease       = "release"
	DecisionReleaseFailed = "release-failed"
	StepReleaseCheck      = "release-check"
	StepReleasePublish    = "release-publish"
)

// ReleasePolicy is owner-controlled. A check verifies only; native git publishes.
type ReleasePolicy struct {
	When    string `json:"when"`
	Check   string `json:"check,omitempty"`
	GitHub  string `json:"github,omitempty"`
	Approve string `json:"approve,omitempty"`
}

func (r ReleasePolicy) Trimmed() ReleasePolicy {
	r.When, r.Check, r.GitHub, r.Approve = strings.TrimSpace(r.When), strings.TrimSpace(r.Check), strings.TrimSpace(r.GitHub), strings.TrimSpace(r.Approve)
	return r
}
func (r ReleasePolicy) validate(p Playbook) error {
	if p.Medium != MediumGit || (p.Land.Way() != LandPush && p.Land.Way() != LandPullRequest) {
		return errors.New("releases need a code project landing by push or pull request")
	}
	if strings.TrimSpace(r.When) == "" || len([]rune(r.When)) > 1000 {
		return errors.New("when to release is required and must fit in 1000 characters")
	}
	if len([]rune(r.Check)) > 500 || strings.ContainsAny(r.Check, "\r\n") {
		return errors.New("release check must be one line of at most 500 characters")
	}
	if r.Approve != "" && r.Approve != ApprovePM {
		return errors.New("release approval must be the owner or pm")
	}
	if r.GitHub != "" && (!githubRepo.MatchString(r.GitHub) || p.Land.Way() != LandPush) {
		return errors.New("release GitHub repository must be owner/name and is only for push landing")
	}
	return nil
}

// Version helpers are shared with the git adapter.
func ValidVersion(v string) bool      { return releaseversion.Valid(v) }
func CompareVersions(a, b string) int { return releaseversion.Compare(a, b) }
func ReleaseVersion(proposed, latest string) (string, error) {
	return releaseversion.Next(proposed, latest)
}

type ReleaseProposal struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
}

// ReleaseContext is fetched by the daemon, never supplied by the PM.
type ReleaseContext struct {
	Commit  string // Landed target seen by the PM; pins the proposed contents.
	Highest string // Includes version tags outside the landed target.
	Latest  string
	Commits []string
	Count   int
	Error   string
}
type ReleaseRun struct {
	ReleaseProposal
	Policy     ReleasePolicy `json:"policy"`
	Land       LandPolicy    `json:"land"`
	Commits    []string      `json:"commits"`
	ApprovedBy string        `json:"approved_by,omitempty"`
	State      string        `json:"state"`
	Commit     string        `json:"commit,omitempty"`
	Started    time.Time     `json:"started,omitempty"`
	DecisionID string        `json:"decision_id,omitempty"`
	LocalTag   bool          `json:"local_tag,omitempty"`
	Waiting    string        `json:"waiting,omitempty"`
}
type ReleaseRecord struct {
	Version    string    `json:"version"`
	Commit     string    `json:"commit"`
	Notes      string    `json:"notes"`
	At         time.Time `json:"at"`
	ApprovedBy string    `json:"approved_by"`
	Published  string    `json:"published,omitempty"`
	Note       string    `json:"note,omitempty"`
}

func proposeRelease(v *Snapshot, p *Project, in ReleaseProposal, seen ReleaseContext, now time.Time) error {
	if p.Playbook == nil || p.Playbook.Release == nil {
		return errors.New("releases are not configured")
	}
	if p.Release != nil {
		return ErrConflict
	}
	if seen.Error != "" {
		return errors.New(seen.Error)
	}
	if seen.Count == 0 {
		return errors.New("nothing has landed since the latest version tag")
	}
	version, err := ReleaseVersion(strings.TrimSpace(in.Version), seen.Latest)
	if err != nil {
		return err
	}
	if seen.Highest != "" && CompareVersions(version, seen.Highest) <= 0 {
		return fmt.Errorf("release version must be above occupied tag %s: %w", seen.Highest, ErrConflict)
	}
	if strings.TrimSpace(in.Notes) == "" || len([]rune(in.Notes)) > 2000 {
		return errors.New("release notes are required and must fit in 2000 characters")
	}
	in.Version, in.Notes = version, strings.TrimSpace(in.Notes)
	p.Release = &ReleaseRun{ReleaseProposal: in, Policy: *p.Playbook.Release, Land: p.Playbook.Land, Commits: seen.Commits, Commit: seen.Commit, State: "proposed"}
	if p.Release.Policy.Approve == ApprovePM {
		p.Release.State, p.Release.ApprovedBy = "approved", "pm"
	} else {
		releaseDecision(v, p, now, false, "")
	}
	record(v, now, p.ID, "release.proposed", "Proposed "+version+": "+in.Notes)
	return nil
}
func releaseDecision(v *Snapshot, p *Project, now time.Time, failed bool, detail string) {
	r := p.Release
	kind, title, choices := DecisionRelease, "Release "+r.Version, []string{"Release " + r.Version, "Not now"}
	if failed {
		kind, title, choices = DecisionReleaseFailed, "Release "+r.Version+" stopped", []string{"Try again", "Leave it"}
	}
	d := Decision{ID: uid(), ProjectID: p.ID, Kind: kind, Title: title, Context: r.Notes + "\n\nIncluded:\n" + strings.Join(r.Commits, "\n") + "\n\n" + detail, Choices: choices, Status: DecisionOpen, CreatedAt: now}
	v.Decisions = append(v.Decisions, d)
	r.DecisionID = d.ID
	record(v, now, p.ID, "decision.opened", title)
}
func resolveRelease(v *Snapshot, d *Decision, answer string, now time.Time) {
	p := project(v, d.ProjectID)
	if p == nil || p.Release == nil || p.Release.DecisionID != d.ID {
		return
	}
	r := p.Release
	r.DecisionID = ""
	switch answer {
	case "Release " + r.Version:
		r.State, r.ApprovedBy = "approved", "owner"
	case "Try again":
		if r.State != "publishing" {
			r.State = "approved"
		}
		r.Waiting = ""
	default:
		if d.Kind == DecisionReleaseFailed && r.LocalTag {
			finishRelease(v, p, now, "", "tag not published")
		} else {
			p.Release = nil
		}
	}
}
func releaseHeld(v *Snapshot, p *Project) string {
	if p.Paused || p.pausedLanding() != "" {
		return "landing is paused"
	}
	for _, t := range v.Tasks {
		if t.ProjectID == p.ID && (t.Status == TaskLanding || t.Delivering != nil) {
			return "a landing is in progress"
		}
	}
	if len(p.Claims) > 0 {
		return "another project turn is in progress"
	}
	return ""
}

// ReleaseHeld lets the loop avoid repository work while a release is held.
func ReleaseHeld(v Snapshot, p Project) string { return releaseHeld(&v, &p) }

func (s *Service) WaitRelease(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		if p.Release == nil {
			return nil
		}
		why := releaseHeld(v, p)
		if why != "" && p.Release.Waiting != why {
			p.Release.Waiting = why
			record(v, s.now().UTC(), id, "release.waiting", "Release waits: "+why)
		}
		return nil
	})
}

// ClaimRelease checks the landing gate and takes a project claim atomically.
func (s *Service) ClaimRelease(ctx context.Context, id, commit string, admit Admit) (Claim, Role, bool, error) {
	var c Claim
	var seat Role
	var ok bool
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		r := p.Release
		if r == nil || r.DecisionID != "" || r.State == "proposed" {
			return nil
		}
		if why := releaseHeld(v, p); why != "" {
			if r.Waiting != why {
				r.Waiting = why
				record(v, s.now().UTC(), id, "release.waiting", "Release waits: "+why)
			}
			return nil
		}
		step := StepReleasePublish
		if r.State != "publishing" && r.Policy.Check != "" {
			step = StepReleaseCheck
			var qa []Role
			for _, role := range p.Playbook.Roles {
				if role.Holds(RoleQA) {
					qa = append(qa, role)
				}
			}
			if len(qa) == 0 {
				releaseDecision(v, p, s.now().UTC(), true, "The team has no QA to run the release check")
				return nil
			}
			var wait *Wait
			seat, wait = freeSeat(v, p.ID, p.Playbook.Roles, qa, busyPeople(v), admit, Wait{})
			if wait != nil {
				return nil
			}
		}
		if r.Commit == "" {
			r.Commit = commit
		}
		if r.Started.IsZero() {
			r.Started = s.now().UTC()
		}
		r.Waiting = ""
		r.State = "publishing"
		if step == StepReleaseCheck {
			r.State = "checking"
		}
		p.Attempt++
		c = Claim{Token: fmt.Sprintf("%s/%s/%d", id, step, p.Attempt), Step: step, Seat: seat.Name, At: s.now().UTC()}
		p.Claims = append(p.Claims, c)
		ok = true
		return nil
	})
	return c, seat, ok, err
}
func (s *Service) ReleaseChecked(ctx context.Context, id string, passed bool, status int, tail string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil || p.Release == nil {
			return ErrConflict
		}
		record(v, s.now().UTC(), id, "release.checked", fmt.Sprintf("Release check exit %d\n%s", status, tail))
		if !passed || status != 0 {
			releaseDecision(v, p, s.now().UTC(), true, fmt.Sprintf("Release check exit %d\n%s", status, tail))
		} else {
			p.Release.State = "approved"
			p.Release.Policy.Check = ""
		}
		return nil
	})
}
func (s *Service) ReleaseFailed(ctx context.Context, id, detail string, localTag bool) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil || p.Release == nil {
			return ErrConflict
		}
		p.Release.LocalTag = localTag
		record(v, s.now().UTC(), id, "release.failed", detail)
		releaseDecision(v, p, s.now().UTC(), true, detail)
		return nil
	})
}
func finishRelease(v *Snapshot, p *Project, now time.Time, published, note string) {
	r := p.Release
	p.UpdatedAt = now
	p.Releases = append([]ReleaseRecord{{Version: r.Version, Commit: r.Commit, Notes: r.Notes, At: now, ApprovedBy: r.ApprovedBy, Published: published, Note: note}}, p.Releases...)
	p.Releases = p.Releases[:min(20, len(p.Releases))]
	p.Release = nil
	record(v, now, p.ID, "release.recorded", "Released "+r.Version+" "+note)
}
func (s *Service) FinishRelease(ctx context.Context, id, published, note string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil || p.Release == nil || p.Release.State != "publishing" || p.Release.DecisionID != "" {
			return ErrConflict
		}
		// This turn's own claim is allowed; every other claim and the landing
		// gate are checked again before the successful record is written.
		f, _ := ctx.Value(fenceKey{}).(fence)
		allowed := *p
		allowed.Claims = nil
		for _, c := range p.Claims {
			if f.project != id || c.Token != f.token {
				allowed.Claims = append(allowed.Claims, c)
			}
		}
		if releaseHeld(v, &allowed) != "" {
			return ErrConflict
		}
		record(v, s.now().UTC(), id, "release.published", "Publication exit 0: tagged "+p.Release.Version+" at "+p.Release.Commit)
		finishRelease(v, p, s.now().UTC(), published, note)
		return nil
	})
}

// UpdateReleaseSettings reconciles unstarted proposals inside EditPlaybook's update.
func UpdateReleaseSettings(v *Snapshot, p *Project, policy *ReleasePolicy) {
	r := p.Release
	if r == nil || !r.Started.IsZero() {
		return
	}
	if policy == nil || (policy.Approve == "" && r.ApprovedBy == "pm") {
		if d := decision(v, r.DecisionID); d != nil && d.Status == DecisionOpen {
			dismiss(v, d, p.UpdatedAt, "Release settings changed")
		}
		if policy == nil {
			p.Release = nil
		} else {
			r.Policy = *policy
			r.State, r.ApprovedBy = "proposed", ""
			releaseDecision(v, p, p.UpdatedAt, false, "")
		}
	}

	if policy != nil {
		r.Policy = *policy
	}
}

// PinRelease records the landed target after its seat has been claimed.
func (s *Service) PinRelease(ctx context.Context, id, commit string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil || p.Release == nil || commit == "" {
			return ErrConflict
		}
		if p.Release.Commit == "" {
			p.Release.Commit = commit
		}
		return nil
	})
}
