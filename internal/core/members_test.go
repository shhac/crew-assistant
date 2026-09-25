package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestAMemberHasANameOfItsOwnAndAFace(t *testing.T) {
	s, _ := fixture(t)
	ada, err := s.SaveMember(testContext, "", MemberInput{Name: " Ada ", Kinds: []string{RoleImplementer}, Engine: "claude", Model: "opus"})
	if err != nil || ada.Name != "Ada" || ada.Avatar.Validate() != nil || ada.Learnings == nil {
		t.Fatalf("member %+v %v", ada, err)
	}
	if !reflect.DeepEqual(ada.Avatar, config.DefaultAvatar("ada")) {
		t.Fatal("a new member's face should come from its name")
	}
	for _, in := range []MemberInput{
		{Name: "ADA", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "Rune", Kind: "manager", Engine: "codex"},
		{Name: "Rune", Kinds: []string{RoleReviewer}, Engine: "gpt"},
		{Name: "", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "Rune", Kinds: []string{RoleReviewer}, Engine: "codex", Avatar: &config.Avatar{Shape: "orb", Background: "#000000", Accent: "javascript:"}},
	} {
		if _, err := s.SaveMember(testContext, "", in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	drawn := config.Avatar{Background: "#101820", Accent: "#ffffff", Marks: []config.Mark{{D: "M10 10\nL118 118", Color: "#ffffff", StrokeWidth: 8}}}
	ada, err = s.SaveMember(testContext, ada.ID, MemberInput{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude", Instructions: "Small commits.", Avatar: &drawn})
	if err != nil || ada.Instructions != "Small commits." || ada.Avatar.Marks[0].D != "M10 10 L118 118" {
		t.Fatalf("update %+v %v", ada, err)
	}
	if _, err = s.SaveMember(testContext, "missing", MemberInput{Name: "Zed", Kinds: []string{RoleQA}, Engine: "codex"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updating a member that does not exist: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Members) != 1 || !strings.HasPrefix(snap.Members[0].AvatarSVG, "<svg") {
		t.Fatalf("the dashboard should see the member drawn: %+v", snap.Members)
	}
	raw, _ := s.store.Snapshot(testContext)
	if raw.Members[0].AvatarSVG != "" {
		t.Fatal("the drawing is rendered on read, never stored")
	}
}

func TestAnEmptyStateHasNoMembersRatherThanNull(t *testing.T) {
	s, _ := fixture(t)
	snap, _ := s.Snapshot(testContext)
	raw, _ := json.Marshal(snap)
	if !strings.Contains(string(raw), `"members":[]`) {
		t.Fatalf("members should read as an empty list: %s", raw)
	}
}

func TestRoleNamesMustDifferByMoreThanCase(t *testing.T) {
	p := Templates["draft"]
	p.Roles = append([]Role{}, p.Roles...)
	p.Roles[1].Name = strings.ToUpper(p.Roles[0].Name)
	if err := p.Validate(); err == nil {
		t.Fatal("two roles named alike were accepted")
	}
}
