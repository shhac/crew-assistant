package core

import "context"

// ObserveDeliveryFailure retains the exact intent and any existing owner answer
// while opening at most one recovery decision for a failed observation.
func (s *Service) ObserveDeliveryFailure(ctx context.Context, id string, expected Task, in DecisionInput) error {
	if err := in.validTaskDecision(); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, fenceKey{}, struct{}{})
	_, err := s.updateTask(ctx, id, func(v *Snapshot, t *Task, _ *Project) (string, error) {
		if expected.Delivering != nil {
			if t.Delivering == nil || t.Delivering.Revision != expected.Delivering.Revision || !t.Delivering.At.Equal(expected.Delivering.At) {
				return "", ErrStale
			}
		} else if !expected.PRMergePending() || !t.PRMergePending() || t.Proposal.Number != expected.Proposal.Number || t.Proposal.MergeRequested != expected.Proposal.MergeRequested {
			return "", ErrStale
		}
		if t.Detail == in.Context && (t.Finished() || t.Status == TaskWaiting && t.DecisionID != "") {
			return "", nil
		}
		t.Detail = in.Context
		if !t.Finished() && !(t.Status == TaskWaiting && t.DecisionID != "") {
			t.ResumeStatus = TaskLanding
			openTaskDecision(v, t, DecisionFailure, in, s.now().UTC())
		}
		return in.Context, nil
	})
	return err
}

// RecordDeliveryOutcome records evidence for an existing outward intent even
// after Stop revoked its role claim. It never grants authority to resume work.
func (s *Service) RecordDeliveryOutcome(ctx context.Context, id string, intent Delivering, failure string, refused bool) error {
	ctx = context.WithValue(ctx, fenceKey{}, struct{}{})
	_, err := s.updateTask(ctx, id, func(_ *Snapshot, t *Task, _ *Project) (string, error) {
		if t.Delivering == nil || t.Delivering.Revision != intent.Revision || !t.Delivering.At.Equal(intent.At) {
			return "", ErrStale
		}
		t.Delivering.Failure, t.Delivering.Refused = failure, refused
		return "", nil
	})
	return err
}

// FailDelivery records a confirmed non-delivery and its recovery choice together.
// A closed proposal is retired in the same transition, so one retry replaces it.
func (s *Service) FailDelivery(ctx context.Context, id string, revision int, closed bool, in DecisionInput) error {
	if err := in.validTaskDecision(); err != nil {
		return err
	}
	_, err := s.updateTask(ctx, id, func(v *Snapshot, t *Task, _ *Project) (string, error) {
		if (t.Delivering == nil || t.Delivering.Revision != revision) && !t.PRMergePending() {
			return "", ErrStale
		}
		t.Delivering = nil
		if t.Proposal != nil {
			t.Proposal.MergeRequested = ""
		}
		if closed && t.Proposal != nil {
			t.Proposal.Number, t.Proposal.URL, t.Proposal.MergeApproved = 0, "", 0
			t.Proposal.Observed = nil
		}
		if !t.Finished() {
			t.ResumeStatus = TaskLanding
			openTaskDecision(v, t, DecisionFailure, in, s.now().UTC())
		}
		return "", nil
	})
	return err
}
