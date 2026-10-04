package core

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetiredSpriteDerivedBlockPreservesHistoricalAndUnrelatedInstructions(t *testing.T) {
	m := Member{ID: "synthetic"}
	v := Snapshot{RetiredSpriteMembers: []string{m.ID}}
	r := Role{Member: m.ID, Instructions: "Template instructions\n\n" + LegacySpriteInstructions + "\n\nPersonality"}
	original := r.Instructions
	if got := v.EffectiveSeatInstructions(r); got != "Template instructions\n\nPersonality" {
		t.Fatal(got)
	}
	if r.Instructions != original {
		t.Fatal("historical record changed")
	}
	for _, instructions := range []string{LegacySpriteInstructions + "\nEdited", "prefix " + LegacySpriteInstructions, "Sprite work unrelated"} {
		r.Instructions = instructions
		if v.EffectiveSeatInstructions(r) != instructions {
			t.Fatal("altered block removed")
		}
	}
	r.Instructions = original
	v.RetiredSpriteMembers = nil
	if v.EffectiveSeatInstructions(r) != original {
		t.Fatal("removed unretired block")
	}
	v.RetiredSpriteMembers = []string{"other"}
	if v.EffectiveSeatInstructions(r) != original {
		t.Fatal("removed unrelated member block")
	}
}

func TestSpriteRetirementSaveDeleteRestart(t *testing.T) {
	s, c := fixture(t)
	for _, text := range []string{LegacySpriteInstructions, LegacySpriteInstructions + "\nEdited", "Unrelated"} {
		m, err := s.SaveMember(testContext, "", MemberInput{Name: "Ash", Kinds: []string{RoleDesigner}, Engine: "codex", Instructions: text})
		if err != nil {
			t.Fatal(err)
		}
		expected := LegacySpriteInstructions
		in := MemberInput{Name: m.Name, Kinds: m.Kinds, Engine: m.Engine, ExpectedInstructions: &expected}
		seat := Role{Member: m.ID, Instructions: "Template\n\n" + text}
		if err := s.store.update(testContext, func(v *Snapshot) error {
			v.Tasks = append(v.Tasks, Task{ID: m.ID, Roles: []Role{seat}, Playbook: &Playbook{Roles: []Role{seat}}})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(testContext)
		cancel()
		if _, err = s.SaveMember(ctx, m.ID, in); err == nil {
			t.Fatal("cancelled save succeeded")
		}
		before, _ := s.Snapshot(testContext)
		if len(before.RetiredSpriteMembers) != 0 {
			t.Fatal("failed save retired lessons")
		}
		_, err = s.SaveMember(testContext, m.ID, in)
		if text == LegacySpriteInstructions {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal("cleanup did not preserve edited instructions", err)
		}
		snap, _ := s.Snapshot(testContext)
		current, _ := snap.Member(m.ID)
		wantLive := text
		if text == LegacySpriteInstructions {
			wantLive = ""
		}
		if current.Instructions != wantLive {
			t.Fatal("live instructions lost", current.Instructions)
		}
		want := seat.Instructions
		if text == LegacySpriteInstructions {
			want = "Template"
		}
		if got := snap.EffectiveSeatInstructions(seat); got != want {
			t.Fatal(got)
		}
		// Reconcile a repeated cleanup by rereading the current field.
		expected = current.Instructions
		if text != LegacySpriteInstructions {
			in.Instructions = current.Instructions
		}
		if _, err = s.SaveMember(testContext, m.ID, in); err != nil {
			t.Fatal(err)
		}
		again, _ := s.Snapshot(testContext)
		if !reflect.DeepEqual(snap.RetiredSpriteMembers, again.RetiredSpriteMembers) {
			t.Fatal("not idempotent")
		}
		if err = s.DeleteMember(testContext, m.ID); err != nil {
			t.Fatal(err)
		}
		st, err := Open(filepath.Join(s.StateDirectory(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		restarted := NewService(st, c)
		snap, err = restarted.Snapshot(testContext)
		if err != nil {
			t.Fatal(err)
		}
		if got := snap.EffectiveSeatInstructions(seat); got != want {
			t.Fatal("retirement lost", got)
		}
		for _, task := range snap.Tasks {
			if task.ID == m.ID && (task.Roles[0].Instructions != seat.Instructions || task.Playbook.Roles[0].Instructions != seat.Instructions) {
				t.Fatal("historical instructions rewritten")
			}
		}
		st.Close()
		// Reset only this synthetic fixture's tombstones for the next case.
		if err = s.store.update(testContext, func(v *Snapshot) error { v.RetiredSpriteMembers = nil; return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMemberInstructionsPreconditionProtectsInterveningEdit(t *testing.T) {
	s, _ := fixture(t)
	in := MemberInput{Name: "Ash", Kinds: []string{RoleDesigner}, Engine: "codex", Instructions: LegacySpriteInstructions, Personality: "Keep this", Description: "Keep this too"}
	m, err := s.SaveMember(testContext, "", in)
	if err != nil {
		t.Fatal(err)
	}
	expected := m.Instructions // Owner reads the stopgap, then another save wins.
	cleanup := in
	cleanup.Instructions, cleanup.ExpectedInstructions = "", &expected
	in.Instructions, in.Description = "Owner replacement", "New description"
	if _, err := s.SaveMember(testContext, m.ID, in); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(testContext)
	if _, err := s.SaveMember(testContext, m.ID, cleanup); !errors.Is(err, ErrConflict) {
		t.Fatal("stale cleanup accepted", err)
	}
	after, _ := s.Snapshot(testContext)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("conflict changed member, retirement or activity")
	}
	// Empty expectations are meaningful; omission remains an ordinary save.
	empty := ""
	in.ExpectedInstructions = &empty
	if _, err := s.SaveMember(testContext, m.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatal("empty expectation ignored", err)
	}
	in.ExpectedInstructions = nil
	in.Instructions = ""
	if _, err := s.SaveMember(testContext, m.ID, in); err != nil {
		t.Fatal(err)
	}
	in.ExpectedInstructions = &empty
	if _, err := s.SaveMember(testContext, m.ID, in); err != nil {
		t.Fatal("matching empty expectation refused", err)
	}
}
