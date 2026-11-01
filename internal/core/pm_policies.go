package core

import (
	"context"
	"fmt"
	"maps"
)

// PMPolicyDefaults returns a copy of the authority table.
func PMPolicyDefaults() map[string]bool {
	return map[string]bool{"rescope": true, "accept_leftovers": true, "pause_loop": true, "change_approach": true, "close_duplicate": true}
}

// PMPolicyOn reads only PM authority, never Geoffrey's modes.
func PMPolicyOn(v Snapshot, projectID, policy string) bool {
	fallback, known := PMPolicyDefaults()[policy]
	if !known {
		return false
	}
	p := project(&v, projectID)
	if p == nil {
		return false
	}
	var choices map[string]bool
	if t := v.Team(p.Team); t != nil {
		choices = t.PMPolicies
	} else if p.Settings != nil {
		choices = p.Settings.PMPolicies
	}
	if on, explicit := choices[policy]; explicit {
		return on
	}
	return fallback
}

// SetPMPolicy is owner-only; nil resets an explicit choice to its default.
// scope is team or project. revision fences simultaneous owner controls.
func (s *Service) SetPMPolicy(ctx context.Context, scope, id, policy string, on *bool, revision uint64, by string) error {
	if by != FromOwner {
		return fmt.Errorf("only the owner changes PM policies: %w", ErrConflict)
	}
	if _, ok := PMPolicyDefaults()[policy]; !ok {
		return fmt.Errorf("unknown PM policy %q", policy)
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		var choices *map[string]bool
		var rev *uint64
		switch scope {
		case "team":
			t := v.Team(id)
			if t == nil {
				return ErrNotFound
			}
			choices, rev = &t.PMPolicies, &t.PMPoliciesRevision
		case "project":
			p := project(v, id)
			if p == nil {
				return ErrNotFound
			}
			if p.Team != "" {
				return fmt.Errorf("change this policy on its team: %w", ErrConflict)
			}
			if p.Settings == nil {
				return fmt.Errorf("choose project settings first: %w", ErrConflict)
			}
			choices, rev = &p.Settings.PMPolicies, &p.Settings.PMPoliciesRevision
		default:
			return fmt.Errorf("unknown policy scope %q", scope)
		}
		if *rev != revision {
			return fmt.Errorf("PM policies changed; refresh first: %w", ErrConflict)
		}
		next := maps.Clone(*choices)
		if next == nil {
			next = map[string]bool{}
		}
		if on == nil {
			delete(next, policy)
		} else {
			next[policy] = *on
		}
		*choices = next
		*rev++
		pid := ""
		if scope == "project" {
			pid = id
		}
		choice := "Default"
		if on != nil {
			choice = "Off"
			if *on {
				choice = "On"
			}
		}
		record(v, s.now().UTC(), pid, "pm.policy", fmt.Sprintf("Owner set %s %s PM policy %s to %s (revision %d)", scope, id, policy, choice, *rev))
		return nil
	})
}
