package work

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// designerTools are the designer's tools for its turn on a task with it.
func designerTools(t *testing.T, a *Loop, held core.Task, workDir string) roleTools {
	t.Helper()
	designer, _ := held.Designer()
	tools := a.toolsFor(held, core.RoleDesigner, designer)
	tools.workDir = workDir
	return tools
}

func attachArgs(name, content, path string) map[string]string {
	return map[string]string{"name": name, "content": content, "path": path}
}

// The implementer asks for design input twice; the designer makes each
// answer the current design and attaches a mockup to the first. Everyone
// who works on the task afterwards is told the second is the target, and
// can open the first's file, marked as superseded.
func TestTheCurrentDesignIsTheTargetAndEveryRoleCanOpenItsFiles(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign, askDesign}, designs: []string{
		`{"input": "Casual, in short lines.", "current": "this", "escalate": null}`,
		`{"input": "Formal after all.", "current": "this", "escalate": null}`,
	}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	tools := designerTools(t, a, held, t.TempDir())
	if !slices.Contains(toolNames(tools), "attach_file") {
		t.Fatalf("the designer has no attach_file: %v", toolNames(tools))
	}
	if got := callTool(t, tools, "attach_file", attachArgs("card.svg", `<svg xmlns="http://www.w3.org/2000/svg"/>`, "")); got.IsError || !strings.Contains(got.Content, "Attached card.svg") {
		t.Fatalf("attach: %+v", got)
	}
	task = settle(t, a)
	if len(task.Design) != 2 || task.Design[0].N != 1 || !task.Design[0].Marked || task.CurrentDesign != task.Design[1].ID || len(task.Attachments) != 1 {
		t.Fatalf("designs %+v, current %q, attachments %+v", task.Design, task.CurrentDesign, task.Attachments)
	}
	if got := callTool(t, tools, "attach_file", attachArgs("late.svg", "<svg/>", "")); !got.IsError || !strings.Contains(got.Content, "has moved on") {
		t.Fatalf("attached after the input was given: %+v", got)
	}
	designers := turns(runner, "asks for your design input")
	if len(designers) != 2 || !strings.Contains(designers[1].Prompt, "The current design is design 1.") || !strings.Contains(designers[0].Prompt, "There is no current design yet.") || !strings.Contains(designers[0].Prompt, `"current": "this"`) {
		t.Fatalf("the designer should be told which design is current: %d turns", len(designers))
	}
	dir := a.Core.AttachmentsDirectory(task.ID)
	file := filepath.Join(dir, task.Attachments[0].ID) + ": card.svg, with design 1, superseded: not current and not the target"
	writes := writerTurns(runner)
	reviews := turns(runner, "Judge the draft strictly")
	if len(writes) != 3 || len(reviews) != 1 {
		t.Fatalf("%d writer turns, %d reviews", len(writes), len(reviews))
	}
	for name, spec := range map[string]roles.Spec{"implementer": writes[2], "reviewer": reviews[0]} {
		current := strings.Index(spec.Prompt, "The current design, design 2, is the target")
		superseded := strings.Index(spec.Prompt, "Superseded designs: not current and not the target")
		if current < 0 || superseded < current || !strings.Contains(spec.Prompt[current:superseded], "Formal after all.") || !strings.Contains(spec.Prompt[superseded:], "Casual, in short lines.") || !strings.Contains(spec.Prompt[superseded:], "Attached: card.svg") {
			t.Errorf("%s was not told design 2 is the target: %s", name, spec.Prompt)
		}
		if !slices.Contains(spec.Read, dir) || !strings.Contains(spec.Instructions, file) {
			t.Errorf("%s cannot open the attachments: read %v\n%s", name, spec.Read, spec.Instructions)
		}
	}
	if slices.Contains(writes[0].Read, dir) {
		t.Error("a task with no attachments yet was given their directory")
	}
	reviewer := a.toolsFor(task, core.RoleReviewer, core.Role{Name: "Rune"})
	if slices.Contains(toolNames(reviewer), "attach_file") {
		t.Fatal("a reviewer was offered attach_file")
	}
	if got := callTool(t, reviewer, "attach_file", attachArgs("x.svg", "<svg/>", "")); !got.IsError {
		t.Fatal("a reviewer attached a file")
	}
	read := callTool(t, reviewer, "read_task", map[string]string{"task_id": task.ID}).Content
	if !strings.Contains(read, "Current design, the target: design 2 by Dee: Formal after all.") || !strings.Contains(read, "- superseded designs, not the target: 1") || !strings.Contains(read, "Attachments: 1") {
		t.Fatalf("read_task: %s", read)
	}
}

// A role looking the task up reads where the current design's files are
// kept, so it can open the target's artefacts; another task's role reads
// only their names, since its turn can't open them.
func TestReadTaskListsTheCurrentDesignsFiles(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}, designs: []string{`{"input": "A sidebar.", "current": "this", "escalate": null}`}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	if got := callTool(t, designerTools(t, a, held, t.TempDir()), "attach_file", attachArgs("sidebar.svg", "<svg/>", "")); got.IsError {
		t.Fatal(got.Content)
	}
	task = settle(t, a)
	if len(task.Attachments) != 1 || task.CurrentDesign != task.Design[0].ID {
		t.Fatalf("attachments %+v, current %q", task.Attachments, task.CurrentDesign)
	}
	path := filepath.Join(a.Core.AttachmentsDirectory(task.ID), task.Attachments[0].ID)
	own := callTool(t, a.toolsFor(task, core.RoleReviewer, core.Role{Name: "Rune"}), "read_task", map[string]string{"task_id": task.Ref}).Content
	if !strings.Contains(own, "- design 1 file, which you can open: "+path+" (sidebar.svg)") {
		t.Fatalf("read_task on its own task: %s", own)
	}
	other, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Something else"})
	elsewhere := callTool(t, a.toolsFor(other, core.RoleReviewer, core.Role{Name: "Rune"}), "read_task", map[string]string{"task_id": task.ID}).Content
	if !strings.Contains(elsewhere, "- design 1 file: sidebar.svg (on another task") || strings.Contains(elsewhere, path) {
		t.Fatalf("read_task on another task: %s", elsewhere)
	}
}

func TestTheDesignerAttachesOnlyFilesInsideItsWorkspace(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	work, outside := t.TempDir(), t.TempDir()
	var shot bytes.Buffer
	png.Encode(&shot, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	for dir, files := range map[string]map[string][]byte{work: {"mock.png": shot.Bytes(), "fake.png": []byte("words")}, outside: {"secret.png": shot.Bytes()}} {
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(work, "link.png")); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(work, "shots"), 0o700)
	tools := designerTools(t, a, held, work)
	if got := callTool(t, tools, "attach_file", attachArgs("", "", "mock.png")); got.IsError {
		t.Fatalf("a file in the workspace: %s", got.Content)
	}
	for name, args := range map[string]map[string]string{
		"a path out":           attachArgs("", "", "../"+filepath.Base(outside)+"/secret.png"),
		"an absolute path out": attachArgs("", "", filepath.Join(outside, "secret.png")),
		"a symlink out":        attachArgs("", "", "link.png"),
		"a directory":          attachArgs("shots.png", "", "shots"),
		"text posing as PNG":   attachArgs("", "", "fake.png"),
		"a missing file":       attachArgs("", "", "nope.png"),
		"both":                 attachArgs("a.svg", "<svg/>", "mock.png"),
		"neither":              attachArgs("a.svg", "", ""),
		"an unsafe name":       attachArgs("../../a.svg", "<svg/>", ""),
		"a program":            attachArgs("run.sh", "echo hi", ""),
	} {
		if got := callTool(t, tools, "attach_file", args); !got.IsError {
			t.Errorf("%s was attached: %s", name, got.Content)
		}
	}
	snap, _ := a.Core.Snapshot(context.Background())
	now, _ := findTask(snap, "", task.ID)
	if len(now.Attachments) != 1 || now.Attachments[0].Name != "mock.png" || now.Attachments[0].Type != "image/png" || now.Attachments[0].Design != held.Design[0].ID {
		t.Fatalf("attachments %+v", now.Attachments)
	}
}

func TestTheDesignerSaysWhichDesignIsCurrent(t *testing.T) {
	task := core.Task{Design: []core.DesignRequest{{ID: "d1", N: 1, Input: "Tabs"}, {ID: "d2"}}}
	for said, want := range map[string]int{"": 0, "none": 0, " This ": core.CurrentThis, "design 1": 1, "1": 1, "Design #1": 1} {
		if got, err := designCurrent(said, "Input", task); err != nil || got != want {
			t.Errorf("%q: %d %v", said, got, err)
		}
	}
	for _, said := range []string{"design 2", "design 3", "the first", "-1"} {
		if _, err := designCurrent(said, "Input", task); err == nil {
			t.Errorf("%q was taken", said)
		}
	}
	if _, err := designCurrent("this", "", task); err == nil {
		t.Error("empty input was made current")
	}
}

// Every role that works on the task reads the current design first, as the
// target, then superseded designs and advice, each marked as not the
// target; QA's check names the current design.
func TestEveryRoleIsToldWhichDesignIsCurrent(t *testing.T) {
	code := core.Project{Title: "Service", Brief: core.Brief{Goal: "Faster"}, Playbook: &core.Playbook{Medium: core.MediumGit, Check: "make check"}}
	docs := core.Project{Title: "Notes", Brief: core.Brief{Goal: "Clear"}}
	task := core.Task{Objective: "Settings page", Round: 1, CurrentDesign: "d2", Design: []core.DesignRequest{
		{ID: "d1", N: 1, Marked: true, From: "Writer", Question: "Layout?", Designer: "Dee", Input: "Tabs across the top."},
		{ID: "d2", N: 2, Marked: true, From: "Writer", Question: "Layout again?", Designer: "Dee", Input: "A sidebar."},
		{ID: "d3", N: 3, From: "Writer", Question: "Colour?", Designer: "Dee", Input: "Keep the brand blue."},
	}, Attachments: []core.Attachment{{ID: "a1", Name: "sidebar.svg", Design: "d2"}}}
	rev := core.Revision{N: 1}
	reviewer := core.Role{Name: "Rune", Kinds: []string{core.RoleReviewer}}
	for name, prompt := range map[string]string{
		"implementer":   writerPrompt(code, task, "", false),
		"researcher":    researcherPrompt(code, task, nil, nil),
		"code reviewer": checkerPrompt(code, task, rev, reviewer, code.Playbook),
		"reviewer":      reviewerPrompt(docs, task, rev),
		"designer":      designerPrompt(code, task, core.DesignRequest{From: "Writer", Step: core.TaskWriting, Question: "Spacing?"}, false),
	} {
		current := strings.Index(prompt, "The current design, design 2, is the target")
		superseded := strings.Index(prompt, "Superseded designs: not current and not the target")
		advice := strings.Index(prompt, "Design advice given on this task, not a design to build to")
		if current < 0 || superseded < current || advice < superseded {
			t.Errorf("%s: current %d, superseded %d, advice %d", name, current, superseded, advice)
			continue
		}
		if !strings.Contains(prompt[current:superseded], "A sidebar.") || !strings.Contains(prompt[current:superseded], "Attached: sidebar.svg") || !strings.Contains(prompt[superseded:advice], "Design 1. Writer asked: Layout?") || !strings.Contains(prompt[advice:], "Design 3.") {
			t.Errorf("%s put a design in the wrong place:\n%s", name, prompt)
		}
	}
	qa := checkerPrompt(code, task, rev, core.Role{Name: "QA", Kinds: []string{core.RoleQA}}, code.Playbook)
	if !strings.Contains(qa, "Its current design is design 2 by Dee; any earlier design is superseded and not the target.") {
		t.Fatalf("QA: %s", qa)
	}
	if designer := designerPrompt(code, task, core.DesignRequest{From: "Writer", Question: "Spacing?"}, false); !strings.Contains(designer, "The current design is design 2.") {
		t.Fatalf("designer: %s", designer)
	}
	task.CurrentDesign = ""
	if prompt := writerPrompt(code, task, "", false); strings.Contains(prompt, "is the target") || strings.Contains(checkerPrompt(code, task, rev, core.Role{Name: "QA", Kinds: []string{core.RoleQA}}, code.Playbook), "current design") {
		t.Fatalf("with no current design, none was named: %s", prompt)
	}
}
