package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LandDecision is the PM's decision about landing a task's draft, on a
// project whose landing policy lets the PM decide: land it or hold it, and
// why, in a line the owner reads on the task and in the activity.
type LandDecision struct {
	By   string `json:"by"`
	Land bool   `json:"land"`
	// Method is how a change the PM lands goes onto the target: LandSquash,
	// one commit of the whole change, or LandKeepCommits, the task's own
	// commits as they are. Both only ever move the target forward.
	Method   string    `json:"method,omitempty"`
	Reason   string    `json:"reason"`
	Revision int       `json:"revision"`
	At       time.Time `json:"at"`
}

const (
	// LandByPM marks a landing decision the team's PM made.
	LandByPM = "pm"
	// LandSquash lands a change as one commit on the target, as every push
	// landing did before the PM could choose.
	LandSquash = "squash"
	// LandKeepCommits fast-forwards the target onto the task's own commits.
	LandKeepCommits = "fast-forward"
)

// How words the way a change the PM approved lands.
func (d LandDecision) How() string {
	if d.Method == LandKeepCommits {
		return "keeping its commits"
	}
	return "as one commit"
}

// activityPMLanding records the PM approving or holding a change, with why.
const activityPMLanding = "task.pm_landing"

// PMGates reports whether the team's PM decides whether t's change goes out:
// lands by push, or opens its pull request. The project's current policy
// says so, so the owner taking the decision back applies to work already
// under way, for a task that goes out the same way.
func PMGates(p Project, t Task) bool {
	if p.Playbook == nil || t.Playbook == nil || p.Playbook.Land.Way() != t.Playbook.Land.Way() {
		return false
	}
	land := p.Playbook.Land
	gated := land.ByPM()
	if t.PROpen() {
		gated = land.MergeGate() == ApprovePM
	}
	_, ok := p.PMSeat()
	return gated && ok
}

// pmDeciding reports a task whose signed-off change waits only on the PM's
// decision to land, which the owner may take ahead of it.
func pmDeciding(v *Snapshot, t Task) bool {
	p := project(v, t.ProjectID)
	if t.Status != TaskDeciding || p == nil || !PMGates(*p, t) {
		return false
	}
	return len(signedOff(v, t)) == 0
}

// SignedOff says why a task's change is not signed off for the PM to land,
// or nothing when it is: every reviewer and QA passed its latest draft
// against the current brief, nothing about it waits on the owner, and every
// task it depends on has landed. A stopped dependency has not landed.
func SignedOff(v Snapshot, t Task) []string {
	return signedOff(&v, t)
}

func signedOff(v *Snapshot, t Task) []string {
	if len(t.Revisions) == 0 {
		return []string{"there is no draft yet"}
	}
	r := t.Revisions[len(t.Revisions)-1]
	brief := briefVersion(v, t)
	out := BlockerReasons(t)
	if len(t.RolesOf(RoleQA)) == 0 {
		out = append(out, "the team has no QA to check it")
	}
	for _, checker := range t.Checkers() {
		passed := t.Judged(checker.Name, r.N, brief)
		for _, verdict := range t.Verdicts {
			if verdict.Role == checker.Name && verdict.Revision == r.N && t.Counts(verdict, brief) && verdict.Outcome != VerdictPass {
				passed = false
			}
		}
		if !passed {
			out = append(out, fmt.Sprintf("%s has not passed draft %d", checker.Name, r.N))
		}
	}
	if t.DirectionPending > 0 {
		out = append(out, "the implementer has not yet taken in the owner's direction")
	}
	for _, d := range v.Decisions {
		if d.TaskID == t.ID && d.Status == DecisionOpen {
			out = append(out, "a decision for the owner is open: "+d.Title)
		}
	}
	for _, id := range t.DependsOn {
		if dep := task(v, id); dep != nil && dep.Status != TaskLanded {
			out = append(out, fmt.Sprintf("“%s”, which it depends on, has not landed", dep.Objective))
		}
	}
	return out
}

// DecideLanding records the PM's decision about a task's latest draft in the
// same change as what follows from it, so a restart never asks again or
// lands twice. Landing approves that draft and moves the task on to landing,
// and is refused unless the project still lets the PM decide and the change
// is still signed off. Holding opens hold, the owner's decision, so they can
// land it themselves.
func (s *Service) DecideLanding(ctx context.Context, taskID string, d LandDecision, hold DecisionInput) (Task, error) {
	if !d.Land {
		if err := hold.validTaskDecision(); err != nil {
			return Task{}, err
		}
		d.Method = ""
	}
	if d.Land && d.Method == "" {
		d.Method = LandSquash
	}
	if d.Land && d.Method != LandSquash && d.Method != LandKeepCommits {
		return Task{}, fmt.Errorf("a change lands by %s or %s", LandSquash, LandKeepCommits)
	}
	return s.editTaskRecord(ctx, "", taskID, func(t *Task, v *Snapshot) error {
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		if t.Status != TaskDeciding || len(t.Revisions) == 0 || t.Revisions[len(t.Revisions)-1].N != d.Revision {
			return fmt.Errorf("the task moved on while the PM decided: %w", ErrConflict)
		}
		if !PMGates(*p, *t) {
			return fmt.Errorf("the PM no longer decides what goes out in this project: %w", ErrConflict)
		}
		if why := BlockerReasons(*t); len(why) > 0 {
			return fmt.Errorf("%s: %w", strings.Join(why, "; "), ErrConflict)
		}
		now := s.now().UTC()
		d.By, d.At, d.Reason = LandByPM, now, strings.TrimSpace(d.Reason)
		t.LandDecision = &d
		t.UpdatedAt = now
		if t.UsesPRs() {
			d.Method = ""
		}
		if !d.Land {
			recordTask(v, now, t, activityPMLanding, fmt.Sprintf("The PM held %s: %s", t.Objective, d.Reason))
			openTaskDecision(v, t, DecisionDelivery, hold, now)
			derive(v, t)
			return nil
		}
		if why := signedOff(v, *t); len(why) > 0 {
			return fmt.Errorf("it is not signed off: %s: %w", strings.Join(why, "; "), ErrConflict)
		}
		approvedBefore := t.Approved
		t.Approved = d.Revision
		t.Status, t.DecisionID, t.ResumeStatus, t.Detail = TaskLanding, "", "", "Landing"
		// Only approved so far: catching up, QA on the merged result and the
		// push are still to come, and "landed" is said once they succeed.
		approved := fmt.Sprintf("The PM approved %s to land %s: %s", t.Objective, d.How(), d.Reason)
		switch {
		case t.PROpen():
			// Merging is approved apart from the opening it already had.
			t.Approved = approvedBefore
			t.Proposal.MergeApproved = d.Revision
			approved = fmt.Sprintf("The PM approved merging pull request #%d for %s: %s", t.Proposal.Number, t.Objective, d.Reason)
		case t.UsesPRs():
			approved = fmt.Sprintf("The PM approved opening a pull request for %s: %s", t.Objective, d.Reason)
		}
		recordTask(v, now, t, activityPMLanding, approved)
		derive(v, t)
		return nil
	})
}

// LandAheadOfPM is the owner landing a signed-off change themselves while it
// waits on the PM's decision: their approval, as if they had been asked.
func (s *Service) LandAheadOfPM(ctx context.Context, projectID, taskID string) (Task, error) {
	return s.editTaskRecord(ctx, projectID, taskID, func(t *Task, v *Snapshot) error {
		if !pmDeciding(v, *t) {
			if why := signedOff(v, *t); t.Status == TaskDeciding && len(why) > 0 {
				return fmt.Errorf("it is not signed off: %s: %w", strings.Join(why, "; "), ErrConflict)
			}
			return fmt.Errorf("it is not waiting on the PM's decision to land: %w", ErrConflict)
		}
		now := s.now().UTC()
		latest := t.Revisions[len(t.Revisions)-1].N
		said := "You're landing " + t.Objective + " ahead of the PM"
		switch {
		case t.PROpen():
			t.Proposal.MergeApproved = latest
			said = fmt.Sprintf("You're merging pull request #%d for %s ahead of the PM", t.Proposal.Number, t.Objective)
		case t.UsesPRs():
			t.Approved = latest
			said = "You're opening a pull request for " + t.Objective + " ahead of the PM"
		default:
			t.Approved = latest
		}
		t.LandDecision, t.LandingFailures = nil, nil
		t.Status, t.DecisionID, t.ResumeStatus, t.Detail, t.UpdatedAt = TaskLanding, "", "", "Landing", now
		recordTask(v, now, t, "task.landing", said)
		derive(v, t)
		return nil
	})
}
