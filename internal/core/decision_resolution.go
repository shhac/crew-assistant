package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// ChooseDecision records the owner picking one of a decision's own choices.
// Only a choice made this way can approve, stop or retry; the loop reads any
// other answer as the owner's words.
// Split it also needs the team and owner parts, stored with the answer.
func (s *Service) ChooseDecision(ctx context.Context, id, choice string, by string, split ...*OwnerSplit) (Decision, error) {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return Decision{}, errors.New("choose one of the decision's choices")
	}
	return s.finishDecision(ctx, id, choice, DispositionChoice, "", by, split...)
}

// AnswerDecision records the owner's own words, even when they happen to
// spell one of the choices.
func (s *Service) AnswerDecision(ctx context.Context, id, answer string, by string) (Decision, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > 16*1024 {
		return Decision{}, errors.New("answer is required and must be at most 16 KiB")
	}
	return s.finishDecision(ctx, id, answer, DispositionCustom, "", by)
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
	return s.finishDecision(ctx, id, "", DispositionDismissed, reason, "")
}
func (s *Service) finishDecision(ctx context.Context, id, answer, disposition, reason string, by string, splits ...*OwnerSplit) (Decision, error) {
	var out Decision
	var beforeUpgrade Decision
	var afterCommit func() error
	var upgradeTarget string
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
		if d.Kind == DecisionUpgradeAvailable {
			target := upgradeDecisionVersion(*d)
			if target == "" || target != v.Update.DecisionVersion {
				return fmt.Errorf("this update notice has been superseded: %w", ErrConflict)
			}
		}
		var split *OwnerSplit
		if len(splits) > 0 {
			split = splits[0]
		}
		if disposition == DispositionChoice && answer == ChoiceSplit {
			if d.OwnerStep == nil || split == nil {
				return errors.New("splitting needs an owner step and both parts; choose Split it on the dashboard to edit them")
			}
			cleaned := OwnerSplit{Team: text.Clip(strings.TrimSpace(split.Team), maxCriterion), Owner: text.Clip(strings.TrimSpace(split.Owner), maxCriterion)}
			if cleaned.Team == "" || cleaned.Owner == "" {
				return errors.New("both split parts are required")
			}
			if cleaned.Team == cleaned.Owner {
				return errors.New("team and owner parts must be different")
			}
			if t := task(v, d.TaskID); t != nil && slices.Contains(t.OwnerChecks, cleaned.Team) {
				return errors.New("the team part is already an owner check")
			}
			d.Split = &cleaned
		} else if split != nil {
			return errors.New("a split requires the Split it choice")
		}
		if d.Kind == DecisionRunRecipe && disposition == DispositionChoice && answer == ChoiceUseRecipe {
			if err := acceptRunRecipe(v, d); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		if disposition == DispositionChoice && (d.Kind == DecisionUpgradeAvailable && answer == UpgradeChoice(v.Update.DecisionVersion) || d.Kind == DecisionUpgradeFailed && strings.HasPrefix(answer, "Try ") && strings.HasSuffix(answer, " again")) {
			version := v.Update.DecisionVersion
			if d.Kind == DecisionUpgradeFailed {
				version = strings.TrimSuffix(strings.TrimPrefix(answer, "Try "), " again")
			}
			hook := s.upgradeHook()
			if hook == nil {
				return errors.New("this install cannot self-upgrade; upgrade by hand")
			}
			if v.Update.PendingRequest != nil {
				return fmt.Errorf("an upgrade request is already pending: %w", ErrConflict)
			}
			beforeUpgrade = *d
			upgradeTarget = version
			v.Update.PendingRequest = &UpgradeRequest{DecisionID: id, Version: version, Choice: answer, At: now}
			afterCommit = func() error { return hook(version) }
		}
		if d.Kind == DecisionUpgradeAvailable && (disposition != DispositionChoice || answer != ChoiceUpgradeByHand && answer != UpgradeChoice(v.Update.DecisionVersion)) {
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
			if by != FromOwner && by != FromAssistant {
				return ErrConflict
			}
			if t := task(v, d.TaskID); t != nil && !t.Finished() {
				for i := range t.Blockers {
					b := &t.Blockers[i]
					if b.ID == d.BlockerID && b.ClearedAt == nil {
						b.Outcome = "confirmed"
						if answer == ChoiceDropPrerequisite {
							b.Outcome = "dropped"
						}
						s.clearBlocker(v, t, b, LinkedByOwner, answer)
						b.Settlements[len(b.Settlements)-1].Source = d.ID
						b.Settlements[len(b.Settlements)-1].By = by
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
			d.AnsweredBy = by
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
		if disposition == DispositionCustom {
			if err := s.reopenPrerequisiteAnswer(ctx, v, out, answer, by); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && afterCommit != nil {
		if hookErr := afterCommit(); hookErr != nil {
			restoreErr := s.store.update(context.WithoutCancel(ctx), func(v *Snapshot) error {
				d := decision(v, id)
				if d != nil && d.Status == out.Status && d.Answer == out.Answer {
					if d.Kind == DecisionUpgradeAvailable && v.Update.DecisionVersion != upgradeTarget {
						d.Disposition = DispositionSuperseded
						d.ResolutionReason = "Superseded by a newer update."
					} else {
						*d = beforeUpgrade
						recordOn(v, s.now().UTC(), d.ProjectID, d.TaskID, "decision.opened", d.Title+": upgrade did not start")
					}
				}
				if pending := v.Update.PendingRequest; pending != nil && pending.DecisionID == id {
					v.Update.PendingRequest = nil
				}
				return nil
			})
			return beforeUpgrade, errors.Join(hookErr, restoreErr)
		}
		err = s.store.update(context.WithoutCancel(ctx), func(v *Snapshot) error {
			if pending := v.Update.PendingRequest; pending != nil && pending.DecisionID == id {
				v.Update.PendingRequest = nil
			}
			return nil
		})

	}
	return out, err
}

// dismiss closes d as obsolete, with why.
func dismiss(v *Snapshot, d *Decision, now time.Time, reason string) {
	d.Status, d.Disposition, d.ResolvedAt = DecisionDismissed, DispositionDismissed, &now
	d.ResolutionReason, d.Answer = reason, ""
	recordOn(v, now, d.ProjectID, d.TaskID, "decision.dismissed", d.Title+": "+reason)
}
