package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type ResolveChoiceArgs struct {
	DecisionID string `json:"decision_id"`
	Choice     string `json:"choice"`
}

// DecisionActionDigest fences decision content and its underlying authority.
// Scheduler counters and claims are bookkeeping, not changes to that authority.
func DecisionActionDigest(v Snapshot, id string) (string, error) {
	d := decision(&v, id)
	if d == nil {
		return "", ErrNotFound
	}
	var brief *Brief
	var playbook *Playbook
	var status string
	if p := project(&v, d.ProjectID); p != nil {
		brief, playbook, status = &p.Brief, p.Playbook, p.Status
	}
	var target *Task
	if t := task(&v, d.TaskID); t != nil {
		// Explicit authority projection: derived links, display references,
		// scheduling, notes and unrelated team activity cannot stale a choice.
		copy := Task{
			ID: t.ID, ProjectID: t.ProjectID, Objective: t.Objective, DecisionID: t.DecisionID,
			Criteria: t.Criteria, OwnerChecks: t.OwnerChecks, Status: t.Status,
			Roles: t.Roles, Playbook: t.Playbook, MaxRounds: t.MaxRounds,
			Round: t.Round, Direction: t.Direction, DirectionPending: t.DirectionPending,
			Revisions: t.Revisions, Verdicts: t.Verdicts, TextVersion: t.TextVersion,
			DependsOn: t.DependsOn, Blockers: t.Blockers, Delivering: t.Delivering,
		}
		target = &copy
	}
	data, err := json.Marshal(struct {
		Decision      Decision
		Brief         *Brief
		Playbook      *Playbook
		ProjectStatus string
		Task          *Task
	}{*d, brief, playbook, status, target})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// Only ordinary choice records are supported here. Release, upgrade,
// prerequisite, owner-step and other specialised policies need explicit
// follow-up adapters; general Act is never authority for them.
func decisionChoiceAction(s *Service) LocalAction {
	check := func(v Snapshot, a ConcreteAction) error {
		var args ResolveChoiceArgs
		if err := strictJSON(a.Args, &args); err != nil {
			return err
		}
		d := decision(&v, args.DecisionID)
		if d == nil {
			return ErrNotFound
		}
		if d.Kind != DecisionChoice || d.OwnerStep != nil || d.ProjectID != a.ProjectID || d.TaskID != a.TaskID || a.Commit != "" {
			return errors.New("decision is outside ordinary choice authority")
		}
		if d.Status != DecisionOpen || !slices.Contains(d.Choices, args.Choice) {
			return ErrConflict
		}
		digest, err := DecisionActionDigest(v, d.ID)
		if err != nil {
			return err
		}
		if a.TargetDigest == "" || a.TargetDigest != digest {
			return ErrConflict
		}
		return nil
	}
	return LocalAction{OwnerAllowed: true, Check: check, Apply: func(v *Snapshot, a ConcreteAction, actor AutopilotActorKind) (json.RawMessage, uint64, error) {
		if err := check(*v, a); err != nil {
			return nil, 0, err
		}
		var args ResolveChoiceArgs
		_ = json.Unmarshal(a.Args, &args)
		by := FromAssistant
		if actor == AutopilotOwner {
			by = FromOwner
		}
		_, err := s.finishDecisionUsing(context.Background(), args.DecisionID, args.Choice, DispositionChoice, "", by, func(_ context.Context, fn func(*Snapshot) error) error { return fn(v) })
		return nil, 0, err
	}}
}
