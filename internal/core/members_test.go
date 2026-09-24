package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestAMemberHasANameOfItsOwnAndAFace(t *testing.T) {
	s, _ := fixture(t)
	ada, err := s.SaveMember(testContext, "", MemberInput{Name: " Ada ", Kind: RoleImplementer, Engine: "claude", Model: "opus"})
	if err != nil || ada.Name != "Ada" || ada.Avatar.Validate() != nil || ada.Learnings == nil {
		t.Fatalf("member %+v %v", ada, err)
	}
	if !reflect.DeepEqual(ada.Avatar, config.DefaultAvatar("ada")) {
		t.Fatal("a new member's face should come from its name")
	}
	for _, in := range []MemberInput{
		{Name: "ADA", Kind: RoleReviewer, Engine: "codex"},
		{Name: "Reviewer", Kind: RoleReviewer, Engine: "codex"},
		{Name: "Rune", Kind: "manager", Engine: "codex"},
		{Name: "Rune", Kind: RoleReviewer, Engine: "gpt"},
		{Name: "", Kind: RoleReviewer, Engine: "codex"},
		{Name: "Rune", Kind: RoleReviewer, Engine: "codex", Avatar: &config.Avatar{Shape: "orb", Background: "#000000", Accent: "javascript:"}},
	} {
		if _, err := s.SaveMember(testContext, "", in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	drawn := config.Avatar{Background: "#101820", Accent: "#ffffff", Marks: []config.Mark{{D: "M10 10\nL118 118", Color: "#ffffff", StrokeWidth: 8}}}
	ada, err = s.SaveMember(testContext, ada.ID, MemberInput{Name: "Ada", Kind: RoleImplementer, Engine: "claude", Instructions: "Small commits.", Avatar: &drawn})
	if err != nil || ada.Instructions != "Small commits." || ada.Avatar.Marks[0].D != "M10 10 L118 118" {
		t.Fatalf("update %+v %v", ada, err)
	}
	if _, err = s.SaveMember(testContext, "missing", MemberInput{Name: "Zed", Kind: RoleQA, Engine: "codex"}); !errors.Is(err, ErrNotFound) {
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

func TestLearningsAreKeptWithinABoundAndCanBeForgotten(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Rune", Kind: RoleReviewer, Engine: "codex"})
	add := func(when, text, projectID string) (Member, error) {
		return s.AddLearning(testContext, m.ID, LearnedByOwner, LearningInput{When: when, Text: text, ProjectID: projectID})
	}
	if _, err := add("", strings.Repeat("x", 1501), ""); err == nil {
		t.Fatal("an overlong learning was kept")
	}
	if _, err := add(strings.Repeat("x", 161), "Check them", ""); err == nil {
		t.Fatal("an overlong when was kept")
	}
	if _, err := add("", "Check the migrations", "nowhere"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a learning from a project that does not exist: %v", err)
	}
	for i := 0; i < maxLearnings; i++ {
		if m, _ = add("When "+string(rune('A'+i)), "Learning "+string(rune('A'+i)), p.ID); len(m.Learnings) != i+1 || m.Learnings[i].Source != LearnedByOwner {
			t.Fatalf("learning %d not kept", i)
		}
	}
	if _, err := add("", "One too many", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("the cap should ask to forget one first: %v", err)
	}
	if m, _ = s.ForgetLearning(testContext, m.ID, m.Learnings[0].ID); len(m.Learnings) != maxLearnings-1 {
		t.Fatal("forgetting did not remove it")
	}
	if err := s.DeleteMember(testContext, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMember(testContext, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
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

func TestAMembersOwnLearningsMakeRoomButNeverPushOutTheOwners(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kind: RoleImplementer, Engine: "claude"})
	learn := func(when string) error {
		_, err := s.RecordLearning(testContext, m.ID, "task", LearningInput{When: when, Text: "Do the thing.", ProjectID: p.ID}, []string{"/secret/repo"})
		return err
	}
	if _, err := s.RecordLearning(testContext, m.ID, "task", LearningInput{Text: "No situation"}, nil); err == nil {
		t.Fatal("a member's learning needs to say when it applies")
	}
	if _, err := s.RecordLearning(testContext, m.ID, "task", LearningInput{When: "Paths", Text: "Look in /secret/repo/docs"}, []string{"/secret/repo"}); err == nil {
		t.Fatal("a learning about the project was kept")
	}
	if err := learn("Oldest"); err != nil {
		t.Fatal(err)
	}
	if err := learn("oldest"); !errors.Is(err, ErrConflict) {
		t.Fatalf("the same situation twice: %v", err)
	}
	for i := 1; i < maxLearnings; i++ {
		if _, err := s.AddLearning(testContext, m.ID, LearnedByOwner, LearningInput{Text: fmt.Sprint("Owner ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := learn("Newest"); err != nil {
		t.Fatalf("a full member should make room: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	got := snap.Members[0].Learnings
	if len(got) != maxLearnings || got[len(got)-1].When != "Newest" || slices.ContainsFunc(got, func(l Learning) bool { return l.When == "Oldest" }) {
		t.Fatalf("the oldest learning it taught itself should have made room: %+v", got[len(got)-1])
	}
	if err := learn("One more"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	owners := 0
	for _, l := range snap.Members[0].Learnings {
		if l.Source == LearnedByOwner {
			owners++
		}
	}
	if owners != maxLearnings-1 {
		t.Fatalf("the owner's learnings should all stay, have %d", owners)
	}
	if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return a.Summary == "Ada learned: Newest" }) {
		t.Fatal("a learning should be noted in the project's activity")
	}
}

func TestAMemberFullOfTheOwnersLearningsKeepsThemAll(t *testing.T) {
	s, _ := fixture(t)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kind: RoleImplementer, Engine: "claude"})
	for i := 0; i < maxLearnings; i++ {
		s.AddLearning(testContext, m.ID, LearnedByOwner, LearningInput{Text: fmt.Sprint("Owner ", i)})
	}
	if _, err := s.RecordLearning(testContext, m.ID, "task", LearningInput{When: "Anything", Text: "Something"}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("a member full of the owner's learnings should keep them all: %v", err)
	}
}

// A when is a line of every later task's instructions, so a second line in
// one could forge another learning or an instruction.
func TestALearningsWhenIsOneLine(t *testing.T) {
	s, _ := fixture(t)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kind: RoleImplementer, Engine: "claude"})
	for _, when := range []string{"a\nb", "a\rb"} {
		if _, err := s.AddLearning(testContext, m.ID, LearnedByOwner, LearningInput{When: when, Text: "Do it."}); err == nil || !strings.Contains(err.Error(), "one line") {
			t.Errorf("AddLearning kept when %q: %v", when, err)
		}
		if _, err := s.RecordLearning(testContext, m.ID, "task", LearningInput{When: when, Text: "Do it."}, nil); err == nil || !strings.Contains(err.Error(), "one line") {
			t.Errorf("RecordLearning kept when %q: %v", when, err)
		}
	}
}

func TestALearningWithoutAWhenIsHeadedByItsOpeningWords(t *testing.T) {
	s, _ := fixture(t)
	m, _ := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kind: RoleImplementer, Engine: "claude"})
	text := "Run the whole suite. Not just the package you changed.\nMore detail."
	m, err := s.AddLearning(testContext, m.ID, LearnedByOwner, LearningInput{Text: text})
	if err != nil || m.Learnings[0].When != "Run the whole suite" {
		t.Fatalf("an owner's learning without a when: %+v %v", m.Learnings, err)
	}
	// One kept before a when was always filled in reads with its heading.
	if err := s.store.update(testContext, func(v *Snapshot) error {
		member(v, m.ID).Learnings = append(member(v, m.ID).Learnings, Learning{ID: "legacy", Text: "Name things plainly. Always."})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if got := snap.Members[0].Learnings[1].When; got != "Name things plainly" {
		t.Fatalf("a legacy learning read with when %q", got)
	}
	if got := Heading(strings.Repeat("word ", 40)); len(got) > 125 {
		t.Fatalf("a heading should be clipped: %d bytes", len(got))
	}
}

func TestALearningThatNamesAProjectIsRecognised(t *testing.T) {
	for text, want := range map[string]bool{
		"In ACME-Portal, run the seed first":       true,
		"Ask ops@example.com before deploying":     true,
		"See https://example.com/runbook":          true,
		"Rotate 0123456789abcdef0123456789abcdef":  true,
		"Run the whole suite before finishing":     false,
		"Prefer small commits; e.g. for Zoë's app": false,
	} {
		if got := naming(text, []string{"acme-portal"}) != ""; got != want {
			t.Errorf("%q: names a project = %v, want %v", text, got, want)
		}
	}
}

func TestAStoppedTaskLetsGoOfWhatItsRolesWereTold(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	got, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Roles = []Role{{Name: "Ada", Kind: RoleImplementer, Member: "m", Learnings: []Learning{{Text: "x"}}}}
		t.Status = TaskStopped
		return "", nil
	})
	if err != nil || got.Roles[0].Learnings != nil {
		t.Fatalf("a stopped task should not keep its roles' learnings: %+v %v", got.Roles, err)
	}
}
