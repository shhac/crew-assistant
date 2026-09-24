package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
		now := s.now().UTC()
		d.ResolvedAt = &now
		d.Disposition = disposition
		if disposition == DispositionDismissed {
			d.Status = DecisionDismissed
			d.ResolutionReason = reason
			d.Answer = ""
			record(v, now, d.ProjectID, "decision.dismissed", d.Title+": "+reason)
		} else {
			d.Status = DecisionResolved
			d.Answer = answer
			record(v, now, d.ProjectID, "decision.resolved", d.Title+": "+answer)
		}
		out = *d
		return nil
	})
	return out, err
}
