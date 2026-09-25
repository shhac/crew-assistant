package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
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

func TestDeletingAMemberGivesItsRoleBackButNotTheWorkUnderWay(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	rune, _ := s.SaveMember(testContext, "", MemberInput{Name: "Rune", Kinds: []string{RoleReviewer}, Engine: "claude", Model: "opus"})
	team := Templates["draft"]
	team.Roles = slices.Clone(team.Roles)
	team.Roles[1] = Role{Name: "Rune", Kinds: []string{RoleReviewer}, Engine: "claude", Model: "opus", Member: rune.ID}
	if _, err := s.SetPlaybook(testContext, p.ID, team); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Draft it"}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.NextTask(testContext); err != nil || !found {
		t.Fatalf("start: %v %v", found, err)
	}
	if err := s.DeleteMember(testContext, rune.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	playbook := snap.Projects[0].Playbook
	if !reflect.DeepEqual(playbook.Roles, Templates["draft"].Roles) {
		t.Fatalf("the project's reviewer should be the template's again: %+v", playbook.Roles)
	}
	if err := playbook.Validate(); err != nil {
		t.Fatalf("the team left behind is broken: %v", err)
	}
	task := snap.Tasks[0]
	if task.Roles[1].Member != rune.ID || task.Playbook.Roles[1].Name != "Rune" {
		t.Fatalf("a request under way should keep its team: %+v", task.Roles)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Summary == "Rune left the team for "+p.Title }) {
		t.Fatalf("the change should be in the project's activity: %+v", snap.Activity)
	}
}

func TestARoleGivenBackNeverTakesAnotherRolesName(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	ada, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude"})
	writer, err := s.SaveMember(testContext, "", MemberInput{Name: "writer", Kinds: []string{RoleReviewer}, Engine: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	team := Templates["draft"]
	team.Roles = []Role{
		{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude", Member: ada.ID},
		{Name: "writer", Kinds: []string{RoleReviewer}, Engine: "codex", Member: writer.ID},
	}
	if _, err = s.SetPlaybook(testContext, p.ID, team); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteMember(testContext, ada.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	playbook := snap.Projects[0].Playbook
	if err = playbook.Validate(); err != nil {
		t.Fatalf("the team left behind is broken: %v", err)
	}
	if got := []string{playbook.Roles[0].Name, playbook.Roles[1].Name}; !reflect.DeepEqual(got, []string{"Writer 2", "writer"}) {
		t.Fatalf("roles: %v", got)
	}
	if playbook.Roles[0].Member != "" || playbook.Roles[0].Instructions != Templates["draft"].Roles[0].Instructions {
		t.Fatalf("the implementer should be the template's again: %+v", playbook.Roles[0])
	}
}

func TestDeletingAMemberInASeatOfSeveralRolesGivesBackEach(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	ada, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RolePlanner, RoleImplementer}, Engine: "codex"})
	pam, _ := s.SaveMember(testContext, "", MemberInput{Name: "Pam", Kinds: []string{RolePM}, Engine: "claude"})
	code := Templates["code"]
	team := code
	team.Repo, team.Check = "/work/service", "make check"
	team.Roles = []Role{
		{Name: "Ada", Kinds: []string{RolePlanner, RoleImplementer}, Engine: "codex", Member: ada.ID},
		code.Roles[2],
		code.Roles[3],
		{Name: "Pam", Kinds: []string{RolePM}, Engine: "claude", Member: pam.ID},
	}
	if _, err := s.SetPlaybook(testContext, p.ID, team); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ada.ID, pam.ID} {
		if err := s.DeleteMember(testContext, id); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	if roles := snap.Projects[0].Playbook.Roles; !reflect.DeepEqual(roles, code.Roles) {
		t.Fatalf("the template's planner and implementer should be back, and no PM: %+v", roles)
	}
}

func TestAMemberStaysIfAnyTeamWouldBeLeftBroken(t *testing.T) {
	s, _ := fixture(t)
	good, broken := newProject(t, s), newProject(t, s)
	ada, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude"})
	team := Templates["draft"]
	team.Roles = slices.Clone(team.Roles)
	team.Roles[0] = Role{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude", Member: ada.ID}
	for _, p := range []Project{good, broken} {
		if _, err := s.SetPlaybook(testContext, p.ID, team); err != nil {
			t.Fatal(err)
		}
	}
	// A team that is already invalid can't be left valid by the change.
	if err := s.store.update(testContext, func(v *Snapshot) error {
		project(v, broken.ID).Playbook.MaxRounds = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMember(testContext, ada.ID); err == nil || !strings.Contains(err.Error(), "Ada can't leave the team for "+broken.Title) {
		t.Fatalf("deleting should be refused, saying why: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Members) != 1 {
		t.Fatal("the member was deleted anyway")
	}
	for _, p := range snap.Projects {
		if p.Playbook.Roles[0].Member != ada.ID {
			t.Fatalf("%s's team changed though nothing was deleted: %+v", p.ID, p.Playbook.Roles)
		}
	}
}
