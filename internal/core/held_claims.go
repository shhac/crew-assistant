package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// DecisionHeldClaim tells the owner a step has been held so long that the
// seat it holds is missed elsewhere, and offers to let it go.
const DecisionHeldClaim = "held-claim"

const (
	ChoiceLetGo       = "Let it go"
	ChoiceKeepWaiting = "Keep waiting"
)

// AccountingHold is why a claim is kept after its turn ended but the turn's
// accounting could not be recorded. Its session has ended and it owns no
// command, so letting it go is always safe.
const AccountingHold = "Held: terminal team turn accounting could not be recorded"

// HeldNotice is how long a step is held before the owner hears of it, once.
const HeldNotice = 30 * time.Minute

// HeldClaimsToNotice reports a step held longer than HeldNotice that the
// owner has not heard of, or a notice whose step has since been let go.
func (v Snapshot) HeldClaimsToNotice(now time.Time) bool {
	settle, open := heldClaimNotices(&v, now)
	return len(settle)+len(open) > 0
}

// NoticeHeldClaims opens one decision for each step held longer than
// HeldNotice, naming its task, seat and why, and settles the notices whose
// steps have since been let go.
func (s *Service) NoticeHeldClaims(ctx context.Context, now time.Time) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		settle, open := heldClaimNotices(v, now)
		at := s.now().UTC()
		for _, id := range settle {
			d := decision(v, id)
			d.Status, d.Disposition, d.ResolvedAt = DecisionResolved, DispositionCompleted, &at
			d.ResolutionReason = "It was let go."
			recordOn(v, at, d.ProjectID, d.TaskID, "decision.resolved", d.Title+": "+d.ResolutionReason)
		}
		for _, d := range open {
			d.ID, d.Status, d.CreatedAt = uid(), DecisionOpen, at
			v.Decisions = append(v.Decisions, d)
			recordOn(v, at, d.ProjectID, d.TaskID, "decision.opened", d.Title)
		}
		return nil
	})
}

// heldClaimNotices is the open notices to settle, by ID, and the notices to
// open, one for each step held too long that has had none.
func heldClaimNotices(v *Snapshot, now time.Time) (settle []string, open []Decision) {
	noticed := map[string]bool{}
	for _, d := range v.Decisions {
		if d.Kind != DecisionHeldClaim {
			continue
		}
		noticed[d.Claim] = true
		if d.Status == DecisionOpen && !stillHeld(v, d) {
			settle = append(settle, d.ID)
		}
	}
	consider := func(projectID, taskID, what string, c Claim) {
		if c.Held == "" || noticed[c.Token] || c.HeldAt.IsZero() || now.Sub(c.HeldAt) < HeldNotice {
			return
		}
		open = append(open, heldClaimDecision(projectID, taskID, what, c))
	}
	for _, t := range v.Tasks {
		for _, c := range t.Claims {
			consider(t.ProjectID, t.ID, "“"+t.Objective+"”", c)
		}
	}
	for _, p := range v.Projects {
		for _, c := range p.Claims {
			consider(p.ID, "", "the PM's look at "+p.Title, c)
		}
	}
	return settle, open
}

func heldClaimDecision(projectID, taskID, what string, c Claim) Decision {
	why := strings.TrimSpace(strings.TrimPrefix(c.Held, "Held:"))
	var b strings.Builder
	fmt.Fprintf(&b, "Work on %s is held because %s.", what, why)
	if c.Seat != "" {
		fmt.Fprintf(&b, " That keeps %s busy, so every task needing them waits.", c.Seat)
	}
	recommend := ChoiceKeepWaiting
	if c.Held == AccountingHold {
		recommend = ChoiceLetGo
		b.WriteString(" The turn itself has ended and nothing of it is still running, so letting it go is safe; restarting crew-assistant also lets it go.")
	} else {
		b.WriteString(" Part of it may still be running. Restarting crew-assistant checks again and lets it go once it has stopped; let it go here only if you know it has.")
	}
	over := fmt.Sprintf("for over %d minutes", int(HeldNotice.Minutes()))
	title := what + " has been held " + over
	if c.Seat != "" {
		title = c.Seat + " has been held on " + what + " " + over
	}
	return Decision{ProjectID: projectID, TaskID: taskID, Kind: DecisionHeldClaim, Claim: c.Token, Title: title, Context: b.String(), Recommendation: recommend, Choices: []string{ChoiceLetGo, ChoiceKeepWaiting}}
}

func stillHeld(v *Snapshot, d Decision) bool {
	held := func(c Claim) bool { return c.Token == d.Claim && c.Held != "" }
	if t := task(v, d.TaskID); t != nil {
		return slices.ContainsFunc(t.Claims, held)
	}
	if p := project(v, d.ProjectID); d.TaskID == "" && p != nil {
		return slices.ContainsFunc(p.Claims, held)
	}
	return false
}

// letHeldClaimGo frees the held step d is about, and its seat with it.
func (s *Service) letHeldClaimGo(v *Snapshot, d *Decision) {
	held := func(c Claim) bool { return c.Token == d.Claim && c.Held != "" }
	if t := task(v, d.TaskID); t != nil {
		t.Claims = slices.DeleteFunc(t.Claims, held)
	} else if p := project(v, d.ProjectID); d.TaskID == "" && p != nil {
		if slices.ContainsFunc(p.Claims, held) {
			s.stopPMChat(v, p.ID, d.Claim)
		}
		p.Claims = slices.DeleteFunc(p.Claims, held)
	}
}
