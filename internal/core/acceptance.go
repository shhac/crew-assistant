package core

import (
	"context"
	"time"
)

const (
	ChoiceAcceptDraft    = "Accept this draft"
	ChoiceAcceptFollowUp = "Accept and follow up"
)

// DraftAcceptance records the work the owner accepted despite review findings.
type DraftAcceptance struct {
	Revision     int    `json:"revision"`
	Decision     string `json:"decision"`
	BriefVersion int    `json:"brief_version"`
	TextVersion  int    `json:"text_version"`
}

// MergeValidation retains terminal command evidence for one merged draft.
type MergeValidation struct {
	Ref          string `json:"ref"`
	BriefVersion int    `json:"brief_version"`
	TextVersion  int    `json:"text_version"`
	Revision     int    `json:"revision"`
	Checked      bool   `json:"checked,omitempty"`
	Failure      string `json:"failure,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
}

// FailAcceptedCatchUp records the failure and its owner decision together.
// A conflict retires acceptance before resolution can produce new work.
func (s *Service) FailAcceptedCatchUp(ctx context.Context, taskID string, revision, brief, text int, conflict bool, in DecisionInput) (Decision, error) {
	if err := in.validTaskDecision(); err != nil {
		return Decision{}, err
	}
	var out Decision
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		if !t.AcceptanceStands(p.Brief.Version) || p.Brief.Version != brief || t.TextVersion != text || t.Revisions[len(t.Revisions)-1].N != revision {
			return ErrStale
		}
		if t.Status == TaskWaiting {
			if d := decision(v, t.DecisionID); d != nil && d.Status == DecisionOpen && d.Kind == DecisionFailure && t.ResumeStatus == TaskReviewing && !conflict {
				out = *d
				return nil
			}
			if d := decision(v, t.DecisionID); !conflict || d == nil || d.Status != DecisionOpen || !d.Approves() {
				return ErrStale
			}
			dismiss(v, decision(v, t.DecisionID), s.now().UTC(), "Catch-up conflicted; the draft needs resolution")
		}
		if conflict {
			if t.Proposal != nil {
				t.Proposal.MergeApproved = 0
			}
			t.Acceptance, t.MergeValidation, t.Approved = nil, nil, 0
			t.ResumeStatus = TaskWriting
		} else {
			if t.MergeValidation == nil || t.MergeValidation.Revision != revision {
				return ErrStale
			}
			t.MergeValidation.Failure = in.Context
			t.ResumeStatus = TaskReviewing
		}
		out = openTaskDecision(v, t, DecisionFailure, in, s.now().UTC())
		return nil
	})
	return out, err
}

// matchesWork accepts wholly unstamped decisions saved before version binding.
// Partial stamps still bind the decision to its recorded work.
func (d Decision) matchesWork(t *Task, brief int) bool {
	return (d.Revision == 0 && d.BriefVersion == 0 && d.TextVersion == 0) ||
		(d.Revision == len(t.Revisions) && d.BriefVersion == brief && d.TextVersion == t.TextVersion)
}

// retireStaleDelivery closes approvals against superseded requirements.
func retireStaleDelivery(v *Snapshot, t *Task, now time.Time) bool {
	d := decision(v, t.DecisionID)
	// Owner direction and dismissals must be applied before retirement.
	if d != nil && (d.Status == DecisionDismissed || (d.Status == DecisionResolved && (d.Disposition != DispositionChoice || d.Answer != "Approve"))) {
		return false
	}
	if t.Finished() || len(t.Revisions) == 0 || d == nil || !d.Approves() || d.matchesWork(t, briefVersion(v, *t)) {
		return false
	}
	dismiss(v, d, now, "The work or requirements changed; checking again")
	t.DecisionID, t.ResumeStatus = "", ""
	t.Status, t.Detail = TaskReviewing, "Checking again after the work or requirements changed"
	t.Approved, t.Acceptance, t.MergeValidation, t.LandDecision = 0, nil, nil, nil
	for i := range t.Claims {
		t.Claims[i].Revoked = true
	}
	if t.Proposal != nil {
		t.Proposal.MergeApproved = 0
	}
	return true
}

// ApplyDeliveryApproval checks the answer's versions atomically with approval.
func (s *Service) ApplyDeliveryApproval(ctx context.Context, taskID, decisionID string, apply func(*Task) string) error {
	_, err := s.updateTask(ctx, taskID, func(v *Snapshot, t *Task, _ *Project) (string, error) {
		if t.Finished() || t.Delivering != nil || t.PRMergePending() || t.DecisionID != decisionID {
			return "", ErrStale
		}
		// Integration is a hold, never approval; it also applies to an internal
		// approval continuation that has no owner decision attached.
		if decisionID == "" && (t.NeedsAssetIntegration() || t.NeedsLandingAssetReply()) {
			if t.Proposal != nil {
				t.Proposal.MergeApproved = 0
			}
			t.Approved, t.Acceptance, t.MergeValidation = 0, nil, nil
			t.Status, t.Detail = TaskWriting, "Integrate delivered assets and provenance in a new draft"
			if t.NeedsLandingAssetReply() {
				t.Detail = "Supply production or classification for the pending asset obstacles"
			}
			return t.Detail, nil
		}
		if retireStaleDelivery(v, t, s.now().UTC()) {
			return "", nil
		}
		d := decision(v, t.DecisionID)
		if t.Status != TaskWaiting || d == nil || d.Status != DecisionResolved || d.Disposition != DispositionChoice || !d.Approves() {
			return "", ErrStale
		}
		return apply(t), nil
	})
	return err
}

func (t Task) AcceptanceStands(brief int) bool {
	a := t.Acceptance
	if a == nil || t.Finished() || a.BriefVersion != brief || a.TextVersion != t.TextVersion || t.DirectionPending > 0 || t.Approved != a.Revision || len(t.Revisions) == 0 {
		return false
	}
	n := t.Revisions[len(t.Revisions)-1].N
	for i := len(t.Revisions) - 1; i >= 0; i-- {
		r := t.Revisions[i]
		if r.N != n {
			continue
		}
		if n == a.Revision {
			return true
		}
		if r.CleanMergeOf <= 0 || r.CleanMergeOf >= n {
			return false
		}
		n = r.CleanMergeOf
	}
	return false
}

func (t Task) AcceptanceCheckers(brief int) []Role {
	if !t.AcceptanceStands(brief) {
		return t.Checkers()
	}
	var out []Role
	for _, r := range t.Checkers() {
		if r.Holds(RoleQA) {
			out = append(out, r)
		}
	}
	return out
}

func (t Task) MergeCheckPending(brief int) bool {
	return t.AcceptanceStands(brief) && t.MergeValidation != nil && t.MergeValidation.Revision == t.Revisions[len(t.Revisions)-1].N && !t.MergeValidation.Checked && t.MergeValidation.Failure == ""
}

func (t Task) AcceptedMergeReady(brief int) bool {
	if !t.AcceptanceStands(brief) {
		return false
	}
	n := t.Revisions[len(t.Revisions)-1].N
	if n == t.Acceptance.Revision {
		return true
	}
	v := t.MergeValidation
	if v == nil || v.Revision != n || v.Ref != t.Revisions[len(t.Revisions)-1].Ref || v.BriefVersion != brief || v.TextVersion != t.TextVersion || !v.Checked || v.Failure != "" {
		return false
	}
	for _, r := range t.AcceptanceCheckers(brief) {
		if !t.Judged(r.Name, n, brief) {
			return false
		}
		for i := len(t.Verdicts) - 1; i >= 0; i-- {
			v := t.Verdicts[i]
			if v.Revision == n && t.CheckerGroup(v.Role) == t.CheckerGroup(r.Name) && t.Counts(v, brief) {
				if v.Outcome != VerdictPass {
					return false
				}
				break
			}
		}
	}
	return true
}
