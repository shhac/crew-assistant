package work

import (
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// A book team's prompts name its seats, so the muse is never told about a
// researcher and the PM never about an implementer.
func TestABookTeamsPromptsNameItsSeats(t *testing.T) {
	t.Parallel()
	team := []core.Role{
		{Name: "Muse", Kinds: []string{core.RoleResearcher}, Engine: "claude", Member: "muse", Lane: "Musing"},
		{Name: "Novelist", Kinds: []string{core.RoleImplementer}, Engine: "claude", Member: "novelist", Lane: "Drafting"},
		{Name: "Editor", Kinds: []string{core.RoleReviewer}, Engine: "codex", Member: "editor", Lane: "Editing"},
		{Name: "Publisher", Kinds: []string{core.RoleReviewer}, Engine: "codex", Member: "publisher"},
		{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude", Member: "pim"},
	}
	p := core.Project{ID: "book", Title: "The Lighthouse", Brief: core.Brief{Version: 1, Goal: "Write a short novel"}, Playbook: &core.Playbook{Medium: core.MediumDocuments, Roles: team}}
	draft := core.Revision{N: 1, Files: []string{"chapter-1.md"}, Summary: "Chapter one", BriefVersion: 1}
	task := core.Task{ID: "t", ProjectID: p.ID, Objective: "Write chapter one", Roles: team, Revisions: []core.Revision{draft},
		Verdicts: []core.Verdict{{Revision: 1, Role: "Editor", Outcome: core.VerdictRevise, Summary: "Slow start.", Findings: []core.Finding{{Note: "Open on the storm"}}}}}
	snap := core.Snapshot{Projects: []core.Project{p}, Tasks: []core.Task{{ID: "new", ProjectID: p.ID, Status: core.TaskTriage, Objective: "Write chapter two"}}}
	prompts := map[string]struct {
		prompt string
		names  []string
	}{
		"muse":     {researcherPrompt(p, core.Task{Objective: "Plan chapter one", Roles: team}, nil, nil), []string{"Novelist"}},
		"novelist": {writerPrompt(p, task, "", false), []string{"Editor and Publisher said:"}},
		"editor":   {checkerPrompt(p, task, draft, team[2], p.Playbook), []string{"Editor and Publisher", "revise back to Novelist", "goes back to Muse", "research to Muse"}},
		"pm":       {pmPrompt(snap, p), []string{"Muse", "Novelist", "Editor", "Publisher", "so Muse starts from a clear ask"}},
	}
	for who, got := range prompts {
		for _, name := range got.names {
			if !strings.Contains(got.prompt, name) {
				t.Errorf("%s's prompt doesn't name %q:\n%s", who, name, got.prompt)
			}
		}
		for _, kind := range []string{"the implementer", "the researcher", "the reviewer", "The reviewers"} {
			if strings.Contains(got.prompt, kind) {
				t.Errorf("%s's prompt says %q where a seat has a name:\n%s", who, kind, got.prompt)
			}
		}
	}
}

// A seat named after its kind, as a template's are, still reads by it, so a
// code team's prompts read as they always have.
func TestATemplateSeatReadsByItsKind(t *testing.T) {
	t.Parallel()
	template := core.Templates["code"].Roles
	for kinds, want := range map[string]string{
		core.RoleImplementer: "the implementer",
		core.RoleQA:          "QA",
		core.RoleDesigner:    "the designer",
	} {
		if got := core.SeatWords(template, kinds); got != want {
			t.Errorf("%s reads %q, want %q", kinds, got, want)
		}
	}
	if got := core.SeatWords(template, core.RoleReviewer, core.RoleQA); got != "the reviewer and QA" {
		t.Errorf("the checkers read %q", got)
	}
	seats := []core.Role{{Name: "Claudius", Kinds: []string{core.RoleReviewer}}, {Name: "Claudius #2", Kinds: []string{core.RoleReviewer}}, {Name: "Rune", Kinds: []string{core.RoleReviewer}}, {Name: "Quinn", Kinds: []string{core.RoleQA}}}
	if got := core.SeatWords(seats, core.RoleReviewer, core.RoleQA); got != "Claudius, Rune and Quinn" {
		t.Errorf("seats filled alike should read once: %q", got)
	}
}
