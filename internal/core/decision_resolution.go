package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ChooseDecision records the owner picking one of a decision's own choices.
// Only a choice made this way can approve, stop or retry; the loop reads any
// other answer as the owner's words.
func (s *Service) ChooseDecision(ctx context.Context, id, choice string) (Decision, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return Decision{}, errors.New("choose one of the decision's choices")
	}
	return s.finishDecision(ctx, id, choice, DispositionChoice, "")
}

// AnswerDecision records the owner's own words, even when they happen to
// spell one of the choices.
func (s *Service) AnswerDecision(ctx context.Context, id, answer string) (Decision, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > 16*1024 {
		return Decision{}, errors.New("answer is required and must be at most 16 KiB")
	}
	return s.finishDecision(ctx, id, answer, DispositionCustom, "")
}

// DismissDecision closes an obsolete question with an audit reason. It is not an
// answer, approval or instruction: it never changes or resumes an assignment.
// A worker waiting on this question stays waiting until explicitly instructed.
// Release decisions instead abandon the pending release, as Not now / Leave it.
func (s *Service) DismissDecision(ctx context.Context, id, reason string) (Decision, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4096 {
		return Decision{}, errors.New("dismissal reason is required and must be at most 4 KiB")
	}
	return s.finishDecision(ctx, id, "", DispositionDismissed, reason)
}
func (s *Service) finishDecision(ctx context.Context, id, answer, disposition, reason string) (Decision, error) {
	var out Decision
	err := s.store.update(ctx, func(v *Snapshot) error {
		d := decision(v, id)
		if d == nil {
			return ErrNotFound
		}
		if d.Status != DecisionOpen {
			return fmt.Errorf("decision already closed: %w", ErrConflict)
		}
		if disposition == DispositionChoice && !slices.Contains(d.Choices, answer) {
			return fmt.Errorf("%q is not one of this decision's choices: %w", answer, ErrConflict)
		}
		if d.Kind == DecisionRunRecipe && disposition == DispositionChoice && answer == ChoiceUseRecipe {
			if err := acceptRunRecipe(v, d); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		if d.Kind == DecisionUpgradeAvailable && (disposition != DispositionChoice || answer != ChoiceUpgradeByHand) {
			v.Update.Skipped = v.Update.DecisionVersion
		}
		if d.Kind == DecisionRelease || d.Kind == DecisionReleaseFailed {
			choice := answer
			if disposition != DispositionChoice {
				choice = ""
			}
			resolveRelease(v, d, choice, now)
		}
		if d.Kind == DecisionPRFlow && disposition == DispositionChoice {
			choosePRFlow(v, d.TaskID, answer == ChoiceKeepPR, "You", now)
		}
		if d.Kind == DecisionPrerequisite && disposition == DispositionChoice {
			if t := task(v, d.TaskID); t != nil && !t.Finished() {
				for i := range t.Blockers {
					b := &t.Blockers[i]
					if b.ID == d.BlockerID && b.ClearedAt == nil {
						b.Outcome = "confirmed"
						if answer == ChoiceDropPrerequisite {
							b.Outcome = "dropped"
						}
						s.clearBlocker(v, t, b, LinkedByOwner, answer)
					}
				}
			}
		}
		d.ResolvedAt = &now
		d.Disposition = disposition
		if disposition == DispositionDismissed {
			dismiss(v, d, now, reason)
		} else {
			d.Status = DecisionResolved
			d.Answer = answer
			recordOn(v, now, d.ProjectID, d.TaskID, "decision.resolved", d.Title+": "+answer)
		}
		// The owner's answer to the PM goes to its next look at the list;
		// an answer about one task in triage says which, and answers that
		// come in before the PM looks are all kept.
		if d.Kind == DecisionPMQuestion && d.Status == DecisionResolved {
			if p := project(v, d.ProjectID); p != nil {
				answer := d.Answer
				if t := task(v, d.TaskID); t != nil {
					answer = fmt.Sprintf("About %s (“%s”), you asked: %s The owner answered: %s", t.Label(), t.Objective, d.Context, d.Answer)
				}
				if p.PMDue && p.PMDirection != "" {
					answer = p.PMDirection + "\n" + answer
				}
				p.PMDirection, p.PMDue = answer, true
			}
		}
		out = *d
		return nil
	})
	return out, err
}

// dismiss closes d as obsolete, with why.
func dismiss(v *Snapshot, d *Decision, now time.Time, reason string) {
	d.Status, d.Disposition, d.ResolvedAt = DecisionDismissed, DispositionDismissed, &now
	d.ResolutionReason, d.Answer = reason, ""
	recordOn(v, now, d.ProjectID, d.TaskID, "decision.dismissed", d.Title+": "+reason)
}
