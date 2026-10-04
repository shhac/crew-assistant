package core

import (
	"reflect"
	"testing"
)

func TestConditionalCleanupPreservesInterveningMemberFields(t *testing.T) {
	s, _ := fixture(t)
	in := MemberInput{Name: "Ash", Kinds: []string{RoleDesigner}, Engine: "codex", Instructions: LegacySpriteInstructions, Personality: "Before", Description: "Before"}
	m, err := s.SaveMember(testContext, "", in)
	if err != nil {
		t.Fatal(err)
	}
	expected := m.Instructions
	stale := in
	stale.Instructions, stale.ExpectedInstructions = "", &expected
	in.Name, in.Personality, in.Description, in.Effort = "Ember", "Current personality", "Current description", "high"
	in.Kinds = []string{RoleImplementer}
	in.Browser.On = true
	current, err := s.SaveMember(testContext, m.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SaveMember(testContext, m.ID, stale)
	if err != nil {
		t.Fatal(err)
	}
	current.Instructions = ""
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("cleanup overwrote unrelated edits: got %+v; want %+v", got, current)
	}
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EffectiveSeatInstructions(Role{Member: m.ID, Instructions: LegacySpriteInstructions}) != "" {
		t.Fatal("retirement not recorded")
	}
}
