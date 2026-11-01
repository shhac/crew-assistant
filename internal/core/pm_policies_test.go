package core

import (
	"errors"
	"github.com/shhac/crew-assistant/internal/config"
	"path/filepath"
	"testing"
)

func TestPMPoliciesDefaultIndependentlyAndPreserveExplicitChoices(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	team, err := s.CreateTeam(testContext, TeamInput{Name: "Test team", Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"team", "project"} {
		t.Run(scope, func(t *testing.T) {
			id := p.ID
			if scope == "team" {
				id = team.ID
				_, err = s.SetProjectSetup(testContext, p.ID, ProjectSetup{Team: team.ID})
				if err != nil {
					t.Fatal(err)
				}
			}
			snap, _ := s.Snapshot(testContext)
			for key := range PMPolicyDefaults() {
				if !PMPolicyOn(snap, p.ID, key) {
					t.Fatal(key)
				}
			}
			off, on := false, true
			if err := s.SetPMPolicy(testContext, scope, id, "close_duplicate", &off, 0, RolePM); err == nil {
				t.Fatal("PM enabled own authority")
			}
			if err := s.SetPMPolicy(testContext, scope, id, "close_duplicate", &off, 0, FromOwner); err != nil {
				t.Fatal(err)
			}
			if err := s.SetPMPolicy(testContext, scope, id, "rescope", &on, 0, FromOwner); !errors.Is(err, ErrConflict) {
				t.Fatal("stale revision", err)
			}
			if err := s.SetPMPolicy(testContext, scope, id, "rescope", &on, 1, FromOwner); err != nil {
				t.Fatal(err)
			}
			if scope == "team" {
				if _, err := s.UpdateTeam(testContext, id, TeamUpdate{Name: "Renamed", MaxRounds: 4}); err != nil {
					t.Fatal(err)
				}
			}
			snap, _ = s.Snapshot(testContext)
			for key := range PMPolicyDefaults() {
				if got := PMPolicyOn(snap, p.ID, key); got != (key != "close_duplicate") {
					t.Fatalf("%s=%v", key, got)
				}
			}
			var choices map[string]bool
			if scope == "team" {
				choices = snap.Team(id).PMPolicies
			} else {
				choices = project(&snap, id).Settings.PMPolicies
			}
			if len(choices) != 2 || !choices["rescope"] {
				t.Fatal("explicit On lost", choices)
			}
			if err := s.SetPMPolicy(testContext, scope, id, "close_duplicate", nil, 2, FromOwner); err != nil {
				t.Fatal(err)
			}
			snap, _ = s.Snapshot(testContext)
			if !PMPolicyOn(snap, p.ID, "close_duplicate") {
				t.Fatal("default not restored")
			}
			if PMPolicyOn(snap, p.ID, "unknown") {
				t.Fatal("unknown authority")
			}
			// Teamless test runs after leaving the team and resetting its inherited choices.
			if scope == "team" {
				_, err = s.SetProjectSetup(testContext, p.ID, ProjectSetup{})
				if err != nil {
					t.Fatal(err)
				}
				err = s.store.update(testContext, func(v *Snapshot) error {
					q := project(v, p.ID)
					q.Settings.PMPolicies = nil
					q.Settings.PMPoliciesRevision = 0
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPMPolicyChoicesSurviveStoreReloadAndTeamRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, config.Default())
	p := newProject(t, s)
	team, err := s.CreateTeam(testContext, TeamInput{Name: "Synthetic", FromProject: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	off, on := false, true
	for i, choice := range []struct {
		key string
		on  *bool
	}{{"rescope", &off}, {"pause_loop", &on}} {
		if err = s.SetPMPolicy(testContext, "team", team.ID, choice.key, choice.on, uint64(i), FromOwner); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, config.Default())
	if _, err = s.UpdateTeam(testContext, team.ID, TeamUpdate{Name: "Renamed", MaxRounds: 4}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	settings := snap.Team(team.ID)
	if settings.PMPoliciesRevision != 2 || settings.PMPolicies["rescope"] || !settings.PMPolicies["pause_loop"] || len(settings.PMPolicies) != 2 {
		t.Fatal(settings)
	}
	if PMPolicyOn(snap, p.ID, "rescope") || !PMPolicyOn(snap, p.ID, "close_duplicate") {
		t.Fatal("choices changed")
	}
}

func TestEveryPMPolicyCanBeDisabledIndependently(t *testing.T) {
	for policy := range PMPolicyDefaults() {
		for _, scope := range []string{"team", "project"} {
			t.Run(scope+"/"+policy, func(t *testing.T) {
				s, _ := fixture(t)
				p := newProject(t, s)
				id := p.ID
				if scope == "team" {
					team, err := s.CreateTeam(testContext, TeamInput{Name: "Synthetic", FromProject: p.ID})
					if err != nil {
						t.Fatal(err)
					}
					id = team.ID
				}
				off := false
				if err := s.SetPMPolicy(testContext, scope, id, policy, &off, 0, FromOwner); err != nil {
					t.Fatal(err)
				}
				snap, _ := s.Snapshot(testContext)
				for key := range PMPolicyDefaults() {
					if PMPolicyOn(snap, p.ID, key) != (key != policy) {
						t.Fatalf("changed %s unexpectedly", key)
					}
				}
			})
		}
	}
}

func TestPlaybookReplacementCannotResetOrGrantPMAuthority(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	off := false
	if err := s.SetPMPolicy(testContext, "project", p.ID, "rescope", &off, 0, FromOwner); err != nil {
		t.Fatal(err)
	}
	replacement := clonePlaybook(Templates["draft"])
	replacement.PMPolicies = map[string]bool{"rescope": true, "close_duplicate": false}
	replacement.PMPoliciesRevision = 999
	if _, err := s.SetPlaybook(testContext, p.ID, replacement); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	q := project(&snap, p.ID)
	if PMPolicyOn(snap, p.ID, "rescope") || !PMPolicyOn(snap, p.ID, "close_duplicate") || q.Settings.PMPoliciesRevision != 1 || len(q.Settings.PMPolicies) != 1 {
		t.Fatal("generic settings bypassed owner policy operation", q.Settings)
	}
}
