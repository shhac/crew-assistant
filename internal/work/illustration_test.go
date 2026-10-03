package work

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func illustrationTeam() []core.Role {
	return []core.Role{
		{Name: "Researcher", Engine: "claude", Kinds: []string{core.RoleResearcher}},
		{Name: "Vector", Engine: "claude", Kinds: []string{core.RoleDesigner}},
		{Name: "Raster", Engine: "codex", Kinds: []string{core.RoleDesigner}},
	}
}

func TestIllustrationGuidancePerEngine(t *testing.T) {
	task := core.Task{Roles: illustrationTeam()}
	for _, tc := range []struct {
		generates bool
		wants     []string
	}{
		{true, []string{"first look for the project's existing illustrated assets in the same area", "Generate raster art with your image generation tool", "style, format (such as .webp or .png) and scale", "Hand-written SVG is for mockups, icons, diagrams, simple shapes", "unavailable or fails", "why you chose SVG", "attach_file using generated"}},
		{false, []string{"Draw vector illustrations (SVG)", "characters, ambient motion", "animated SVG or CSS", "Raster (Codex): can generate raster art", "recommend Raster"}},
	} {
		prompt := designerPrompt(core.Project{}, task, core.DesignRequest{}, tc.generates)
		for _, want := range tc.wants {
			if !strings.Contains(prompt, want) {
				t.Errorf("generates %v: missing %q", tc.generates, want)
			}
		}
		if strings.Contains(prompt, "you can make one") || (!tc.generates && (strings.Contains(prompt, "image generation tool") || strings.Contains(prompt, "why you chose SVG"))) {
			t.Fatalf("wrong guidance: %s", prompt)
		}
	}
	task.Roles = append(task.Roles[:2], core.Role{Name: "Painter", Engine: "codex", Kinds: []string{core.RoleImplementer}})
	if prompt := designerPrompt(core.Project{}, task, core.DesignRequest{}, false); !strings.Contains(prompt, "Painter (Codex): can generate raster art") || !strings.Contains(prompt, "recommend Painter") {
		t.Fatal(prompt)
	}
	task.Roles = task.Roles[:2]
	if prompt := designerPrompt(core.Project{}, task, core.DesignRequest{}, false); !strings.Contains(prompt, "No member on this team can generate raster art") {
		t.Fatal(prompt)
	}
}

func TestResearchAndPMSeeImageAbilities(t *testing.T) {
	task := core.Task{Roles: illustrationTeam()}
	project := core.Project{Playbook: &core.Playbook{Roles: task.Roles}}
	for name, prompt := range map[string]string{
		"research": researcherPrompt(project, task, nil, nil),
		"pm":       pmPrompt(core.Snapshot{}, project),
		"pm chat":  pmTeamLine(project),
	} {
		for _, want := range []string{"Raster (Codex): can generate raster art", "Vector (Claude): cannot generate images"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s missing %q: %s", name, want, prompt)
			}
		}
	}
	prompt := researcherPrompt(project, task, nil, nil)
	if !strings.Contains(prompt, `"designer":`) || !strings.Contains(prompt, "name a designer who can generate images") {
		t.Fatal(prompt)
	}
	task.Roles = task.Roles[:2]
	prompt = researcherPrompt(project, task, nil, nil)
	if strings.Contains(prompt, `"designer":`) || !strings.Contains(prompt, "note that in the plan questions") {
		t.Fatal(prompt)
	}
}

func TestResearchDesignerSelectionIsValidated(t *testing.T) {
	seats := (core.Task{Roles: illustrationTeam()}).RolesOf(core.RoleDesigner)
	for _, name := range []string{"Raster", "", "Researcher", "gone"} {
		_, _, question, designer, err := parsePlan(`{"design":"Make a robin","designer":"`+name+`"}`, true, false, seats)
		if name == "Raster" || name == "" {
			if err != nil || question != "Make a robin" || designer != name {
				t.Fatalf("%q: %q %q %v", name, question, designer, err)
			}
		} else if err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	plan, _, question, designer, err := parsePlan(`{"summary":"Add a command","designer":"gone"}`, true, false, seats)
	if err != nil || plan.Summary != "Add a command" || question != "" || designer != "" {
		t.Fatalf("designer without a design request: %+v %q %q %v", plan, question, designer, err)
	}
}

func TestDesignGuideOnlyOffersSelectionAndPlanQuestionsToResearchers(t *testing.T) {
	task := core.Task{Roles: illustrationTeam()}
	asker := core.Role{Name: "Writer", Kinds: []string{core.RoleImplementer}}
	for _, step := range []string{core.TaskWriting, core.TaskResearching} {
		prompt := designGuide(task, asker, step, "ask for input")
		if strings.Contains(prompt, "name a designer who can generate images") != (step == core.TaskResearching) {
			t.Fatal(prompt)
		}
	}
	task.Roles = task.Roles[:2]
	for _, step := range []string{core.TaskWriting, core.TaskResearching} {
		prompt := designGuide(task, asker, step, "ask for input")
		if strings.Contains(prompt, "name a designer who can generate images") || strings.Contains(prompt, "plan questions") != (step == core.TaskResearching) {
			t.Fatal(prompt)
		}
		if step == core.TaskWriting && !strings.Contains(prompt, "note that in your reply") {
			t.Fatal(prompt)
		}
	}
}

func TestDesignerAttachmentSourcesPersistBeforeInput(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prop.svg"), []byte("<svg/>"), 0600); err != nil {
		t.Fatal(err)
	}
	tools := designerTools(t, a, held, dir)
	for _, args := range []map[string]string{attachArgs("mock.svg", "<svg/>", ""), attachArgs("", "", "prop.svg")} {
		if got := callTool(t, tools, "attach_file", args); got.IsError {
			t.Fatal(got.Content)
		}
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := snap.FindTask(task.ID)
	if len(kept.Attachments) != 2 || kept.Attachments[0].Made != "written out by Dee" || kept.Attachments[1].Made != "from the workspace" || kept.Design[0].Input != "" {
		t.Fatalf("%+v", kept)
	}
	entry := designEntry(kept, kept.Design[0])
	for _, want := range []string{"mock.svg (written out by Dee)", "prop.svg (from the workspace)"} {
		if !strings.Contains(entry, want) {
			t.Fatal(entry)
		}
	}
}

func TestResearchRetriesAnInvalidDesignerThenRoutesToNamedSeat(t *testing.T) {
	a, runner, p := plannedCode(t, 6, `{"design":"Make a robin","designer":"Researcher"}`, `{"design":"Make a robin","designer":"Dee"}`)
	seatDesignerOn(t, a, p.ID, "codex")
	task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add art"})
	if err != nil {
		t.Fatal(err)
	}
	held := stepUntil(t, a, task.ID, withDesigner)
	if held.Design[0].For != "Dee" || held.Design[0].Question != "Make a robin" {
		t.Fatalf("%+v", held.Design)
	}
	research := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(research) != 2 {
		t.Fatalf("research turns: %d", len(research))
	}
}
