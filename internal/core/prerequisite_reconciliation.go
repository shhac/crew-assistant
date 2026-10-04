package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// PrerequisiteSettlement preserves the owner's words and their authority.
// Reopening appends history; it never erases a previous answer.
type PrerequisiteSettlement struct {
	Outcome string    `json:"outcome"`
	Answer  string    `json:"answer"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
}

// PrerequisiteComparison is daemon inference, never a researcher's assertion.
type PrerequisiteComparison struct {
	Result  string `json:"result"` // equivalent, different, uncertain
	Blocker string `json:"blocker"`
	Details string `json:"details"`
}
type PrerequisiteComparer func(context.Context, string, bool, []Prerequisite) (PrerequisiteComparison, error)

func (s *Service) SetPrerequisiteComparer(fn PrerequisiteComparer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comparePrerequisite = fn
}

var ErrPrerequisiteComparison = errors.New("prerequisite comparison failed")

var errPrerequisiteStale = errors.New("prerequisite comparison is stale")

type prerequisiteMatch struct {
	what, id string
	question bool
}
type prerequisiteReconciliation struct {
	state                 string
	begun                 bool
	conditions, questions []string
	matches               []prerequisiteMatch
}

func recordPrerequisiteMatches(v *Snapshot, t *Task, matches []prerequisiteMatch, now time.Time) {
	for _, match := range matches {
		for i := range t.Blockers {
			b := &t.Blockers[i]
			if b.ID == match.id && settledPrerequisite(*b) {
				if !match.question && !slices.Contains(b.Aliases, match.what) {
					b.Aliases = append(b.Aliases, match.what)
				}
				recordTask(v, now, t, "prerequisite.reused", fmt.Sprintf("%s reuses %s (%s): %s", match.what, b.ID, b.Outcome, b.Description))
			}
		}
	}
}

func prerequisiteState(t Task) string {
	b, _ := json.Marshal(t.Blockers)
	return string(b)
}

// Recover only ordinary task-linked resolutions, never decision evaluations.
func recoverPrerequisiteAnswers(v *Snapshot, t *Task) {
	for i := range t.Blockers {
		b := &t.Blockers[i]
		if b.Kind != BlockerPrerequisite || b.ClearedAt == nil || b.Outcome == "" || len(b.Settlements) != 0 {
			continue
		}
		for _, d := range v.Decisions {
			if d.TaskID == t.ID && d.BlockerID == b.ID && d.Kind == DecisionPrerequisite && d.Status == DecisionResolved && d.Disposition == DispositionChoice && d.ResolvedAt != nil {
				b.Settlements = append(b.Settlements, PrerequisiteSettlement{b.Outcome, d.Answer, d.AnsweredBy, *d.ResolvedAt, d.ID})
				break
			}
		}
		if len(b.Settlements) == 0 {
			// Legacy task-page clears have attribution but no retained answer.
			// Preserve known metadata without manufacturing the missing words.
			b.Settlements = append(b.Settlements, PrerequisiteSettlement{Outcome: b.Outcome, By: b.ClearedBy, At: *b.ClearedAt, Source: PrerequisiteSettlementID(*b)})
		}
	}
}

// TaskPrerequisites supplies current task records, including available historic
// answers. It does not use the separate evaluation archive.
func TaskPrerequisites(v Snapshot, t Task) []Prerequisite {
	// Avoid modifying snapshot slices while constructing a prompt.
	t.Blockers = append([]Blocker(nil), t.Blockers...)
	recoverPrerequisiteAnswers(&v, &t)
	return taskPrerequisites(t)
}

func prerequisiteWords(s string) string {
	// Case can carry requirements (versions, paths, scope identifiers).
	// Interior whitespace can name a different path or literal identifier.
	// Changed wording goes through conservative comparison.
	return strings.TrimSpace(s)
}

func deterministicPrerequisite(what string, question bool, b Blocker) bool {
	w := prerequisiteWords(what)
	if !question && (w == b.ID || w == "prerequisite "+b.ID) {
		return true
	}
	for _, condition := range append([]string{b.Description}, b.Aliases...) {
		c := prerequisiteWords(condition)
		if !question && w == c {
			return true
		}
		if question && (w == "is "+c+" ready?" || w == "Is "+c+" ready?") {
			return true
		}
	}
	return false
}

func (s *Service) reconcilePrerequisites(ctx context.Context, id string, conditions, questions []string) (prerequisiteReconciliation, error) {
	v, err := s.Snapshot(ctx)
	if err != nil {
		return prerequisiteReconciliation{}, err
	}
	t := task(&v, id)
	if t == nil {
		return prerequisiteReconciliation{}, ErrNotFound
	}
	if t.Status != TaskResearching {
		return prerequisiteReconciliation{}, fmt.Errorf("the task is no longer being researched: %w", ErrConflict)
	}
	out := prerequisiteReconciliation{state: prerequisiteState(*t), begun: len(t.Revisions) > 0}
	if out.begun {
		conditions = nil
	}
	if err := ValidatePrerequisiteConditions(conditions); err != nil {
		return out, err
	}
	if err := ValidatePrerequisiteConditions(questions); err != nil {
		return out, err
	}
	var settled []Prerequisite
	for _, pre := range TaskPrerequisites(v, *t) {
		for _, b := range t.Blockers {
			if b.ID == pre.Blocker && settledPrerequisite(b) {
				settled = append(settled, pre)
				break
			}
		}
	}
	s.mu.RLock()
	compare := s.comparePrerequisite
	s.mu.RUnlock()
	for group, items := range [][]string{conditions, questions} {
		for _, what := range items {
			question := group == 1
			matched := ""
			for _, b := range t.Blockers {
				if settledPrerequisite(b) && deterministicPrerequisite(what, question, b) {
					matched = b.ID
					break
				}
			}
			if matched == "" && len(settled) > 0 && compare != nil {
				bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
				result, err := compare(bounded, what, question, settled)
				cancel()
				if err != nil {
					return out, fmt.Errorf("%w: %v", ErrPrerequisiteComparison, err)
				}
				if result.Result != "equivalent" && result.Result != "different" && result.Result != "uncertain" {
					return out, fmt.Errorf("%w: invalid result", ErrPrerequisiteComparison)
				}
				if result.Result == "equivalent" {
					for _, pre := range settled {
						if pre.Blocker == result.Blocker && strings.TrimSpace(result.Details) != "" {
							matched = pre.Blocker
						}
					}
					if matched == "" {
						return out, fmt.Errorf("%w: missing current settled condition", ErrPrerequisiteComparison)
					}
				}
			}
			if matched != "" {
				out.matches = append(out.matches, prerequisiteMatch{what, matched, question})
			} else if question {
				out.questions = append(out.questions, what)
			} else {
				out.conditions = append(out.conditions, what)
			}
		}
	}
	return out, ctx.Err()
}

// ReopenPrerequisite is an owner coordination action. The condition and exact
// owner instruction must both be supplied; team tools do not expose it.
func (s *Service) ReopenPrerequisite(ctx context.Context, projectID, taskID, id, condition, answer, by string, action PrerequisiteReopen) (Task, error) {
	if by != LinkedByOwner && by != LinkedByAssistant {
		return Task{}, fmt.Errorf("only owner coordination can reopen a prerequisite: %w", ErrConflict)
	}
	if action.Source == "" || action.Settlement == "" {
		return Task{}, fmt.Errorf("owner action identity and expected settlement are required: %w", ErrConflict)
	}
	if strings.TrimSpace(answer) == "" {
		return Task{}, errors.New("the explicit owner instruction is required")
	}
	return s.editTaskRecord(ctx, projectID, taskID, func(t *Task, v *Snapshot) error {
		if t.Finished() || t.Delivering != nil {
			return ErrConflict
		}
		recoverPrerequisiteAnswers(v, t)
		for i := range t.Blockers {
			b := &t.Blockers[i]
			if b.ID != id || b.Kind != BlockerPrerequisite {
				continue
			}
			if strings.TrimSpace(condition) != strings.TrimSpace(b.Description) {
				return ErrConflict
			}
			if by == LinkedByAssistant && action.Source != OwnerInstructionSource(ctx) {
				return ErrConflict
			}
			for _, history := range b.Settlements {
				if history.Outcome == "reopened" && history.Source == action.Source {
					if history.Answer != answer {
						return ErrConflict
					}
					return nil
				}
			}
			if by == LinkedByAssistant && !trustedPrerequisiteInstruction(ctx, v, *b, answer) {
				return ErrConflict
			}
			if by == LinkedByAssistant && !currentPrerequisiteInstruction(ctx, v, *t, *b) {
				return ErrConflict
			}
			if b.ClearedAt == nil || action.Settlement != PrerequisiteSettlementID(*b) {
				return ErrConflict
			}
			s.reopenPrerequisite(v, t, b, answer, by, action.Source)
			return nil
		}
		return ErrNotFound
	})
}

func (s *Service) reopenPrerequisite(v *Snapshot, t *Task, b *Blocker, answer, by, source string) {
	now := s.now().UTC()
	b.Settlements = append(b.Settlements, PrerequisiteSettlement{"reopened", answer, by, now, source})
	b.ClearedAt, b.ClearedBy, b.Outcome = nil, "", ""
	openPrerequisite(v, t, *b, now)
	t.UpdatedAt = now
	if t.Plan != nil {
		t.Plan.Prerequisites = taskPrerequisites(*t)
	}
	if len(t.Revisions) == 0 {
		// A prepared first draft belongs to the turn being superseded.
		// Forget its intent atomically so unfenced restart recovery cannot
		// publish or commit it after the owner has reopened the condition.
		t.Handoff = nil
		// A first turn may still be writing its workspace. Keep its durable
		// launch association until release/reclamation, even after readiness
		// is confirmed again; database revocation alone cannot stop file writes.
		for i := range t.Claims {
			t.Claims[i].Revoked = true
		}
		if t.Status != TaskWaiting {
			t.Status, t.Plan = TaskQueued, nil
		}
	}
	recordTask(v, now, t, "prerequisite.reopened", b.ID+": "+b.Description+": "+answer)
	linksChanged(v, t.ProjectID, by)
	derive(v, t)
}

// An answer can reopen only through this explicit condition-specific form.
// Free text, team responses and assertions in a plan never enter this path.
func (s *Service) reopenPrerequisiteAnswer(ctx context.Context, v *Snapshot, d Decision, answer, by string) error {
	if by != FromOwner && by != FromAssistant {
		return nil
	}
	t := task(v, d.TaskID)
	if t == nil || t.Finished() || t.Delivering != nil {
		return nil
	}
	recoverPrerequisiteAnswers(v, t)
	for i := range t.Blockers {
		b := &t.Blockers[i]
		if b.Kind != BlockerPrerequisite || b.ClearedAt == nil {
			continue
		}
		want := "Prerequisite " + b.ID + " no longer holds: " + b.Description
		if strings.TrimSpace(answer) == want {
			source := "decision:" + d.ID
			if by == FromAssistant {
				if !trustedPrerequisiteInstruction(ctx, v, *b, answer) {
					return nil
				}
				source = "chat:" + ownerInstruction(ctx)
			}
			for _, history := range b.Settlements {
				if history.Outcome == "reopened" && history.Source == source {
					return nil
				}
			}
			if by == FromAssistant && !currentPrerequisiteInstruction(ctx, v, *t, *b) {
				return fmt.Errorf("owner instruction observed an older prerequisite settlement: %w", ErrConflict)
			}
			s.reopenPrerequisite(v, t, b, answer, by, source)
			return nil
		}
	}
	return nil
}

// PrerequisiteInstruction is the settlement an owner message addressed, captured
// in the same transaction that accepts or edits the message.
type PrerequisiteInstruction struct {
	Task       string `json:"task"`
	Blocker    string `json:"blocker"`
	Settlement string `json:"settlement"`
}

func prerequisiteInstruction(v *Snapshot, message string) *PrerequisiteInstruction {
	for _, t := range v.Tasks {
		t.Blockers = append([]Blocker(nil), t.Blockers...)
		recoverPrerequisiteAnswers(v, &t)
		for _, b := range t.Blockers {
			if settledPrerequisite(b) && message == "Prerequisite "+b.ID+" no longer holds: "+b.Description {
				return &PrerequisiteInstruction{t.ID, b.ID, PrerequisiteSettlementID(b)}
			}
		}
	}
	return nil
}

func currentPrerequisiteInstruction(ctx context.Context, v *Snapshot, t Task, b Blocker) bool {
	turn := chatTurn(v, ownerInstruction(ctx))
	if turn == nil || turn.PrerequisiteInstruction == nil {
		return false
	}
	observed := turn.PrerequisiteInstruction
	return observed.Task == t.ID && observed.Blocker == b.ID && observed.Settlement != "" && observed.Settlement == PrerequisiteSettlementID(b)
}

func settledPrerequisite(b Blocker) bool {
	return b.Kind == BlockerPrerequisite && b.ClearedAt != nil && (b.Outcome == "confirmed" || b.Outcome == "dropped")
}

// PrerequisiteReopen binds an owner action to the settlement it observed.
type PrerequisiteReopen struct{ Source, Settlement string }

func PrerequisiteSettlementID(b Blocker) string {
	if b.ClearedAt == nil {
		return ""
	}
	if len(b.Settlements) > 0 {
		return b.Settlements[len(b.Settlements)-1].Source
	}
	return b.ID + "@" + b.ClearedAt.UTC().Format(time.RFC3339Nano) + ":" + b.Outcome
}

type ownerInstructionKey struct{}

// WithOwnerInstruction binds coordination to the daemon-selected chat turn.
// Model arguments cannot choose this identity.
func WithOwnerInstruction(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, ownerInstructionKey{}, turnID)
}
func ownerInstruction(ctx context.Context) string {
	id, _ := ctx.Value(ownerInstructionKey{}).(string)
	return id
}
func trustedPrerequisiteInstruction(ctx context.Context, v *Snapshot, b Blocker, answer string) bool {
	turn := chatTurn(v, ownerInstruction(ctx))
	return turn != nil && turn.Origin == "" && turn.Command == "" && turn.UserMessageID != "" && turn.Status == "running" && turn.Message == answer && answer == "Prerequisite "+b.ID+" no longer holds: "+b.Description
}

const MaxPrerequisiteBytes = 3000

func ValidatePrerequisiteConditions(conditions []string) error {
	if len(conditions) > 20 {
		return errors.New("at most 20 prerequisites may be proposed")
	}
	for _, what := range conditions {
		if len(strings.TrimSpace(what)) > MaxPrerequisiteBytes {
			return errors.New("a prerequisite exceeds 3000 bytes; shorten it without dropping requirements")
		}
	}
	return nil
}

func OwnerInstructionSource(ctx context.Context) string {
	if id := ownerInstruction(ctx); id != "" {
		return "chat:" + id
	}
	return ""
}
