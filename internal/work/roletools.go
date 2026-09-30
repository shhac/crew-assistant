package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

// roleTools are what a role can look up about its project while it works,
// and the changes to tasks it may make: links, its task's requirements and
// notes. Everything it may reach is captured when its turn starts, never
// taken from what the model asks for: the project, its own task, and who it
// is. Its handler runs while the turn runs and calls Core, which takes the
// store's lock; that holds only because no turn ever runs inside a store
// update.
type roleTools struct {
	lp        *Loop
	projectID string
	taskID    string
	// status is the task's status when the turn started. A change is made
	// only while the task is still there, since a turn the owner stopped
	// runs on but must change nothing; core checks it in the same change.
	status string
	// by is how its links are marked; name signs its edits and notes, and
	// kind, the role it plays, decides what it may edit.
	by, name, kind string
	// relations are the links it may make from its own task: the
	// researcher and QA decide what a task waits for; the others only point
	// at related work.
	relations []string
	// manages is the PM looking after the whole list: it names the task in
	// every change, and may queue new ones, at most maxPMQueued a look.
	manages bool
	queued  *int
	// notesOnly limits the PM to leaving notes, for a turn that answers a
	// question and so changes nothing else.
	notesOnly bool
	// design is the request the designer is answering, in a designing turn
	// only: attach_file keeps files with it, reading any it names from
	// workDir.
	design, workDir string
	// generated is where a Codex designer's generated images are found this
	// turn, which attach_file may keep too; nil on an engine that can't
	// generate images.
	generated *generatedImages
	// proposes lets the researcher or the PM of a code project propose how
	// QA runs the app, for the owner to accept; run is the recipe in use.
	proposes bool
	run      *core.RunRecipe
}

// proposing lets the role propose a run recipe, if playbook is a code team's.
func (r roleTools) proposing(playbook *core.Playbook) roleTools {
	if playbook != nil && playbook.Medium == core.MediumGit {
		r.proposes, r.run = true, playbook.Run
	}
	return r
}

// maxPMQueued is how many tasks the PM may queue in one look at the list.
const maxPMQueued = 3

func (lp *Loop) toolsFor(t core.Task, kind string, r core.Role) roleTools {
	tools := roleTools{lp: lp, projectID: t.ProjectID, taskID: t.ID, status: t.Status, by: core.TeamLinker(r.Member, kind), name: seatName(r, kind), kind: kind, relations: relationsFor(kind)}
	if open := t.OpenDesign(); kind == core.RoleDesigner && t.Status == core.TaskDesigning && open != nil {
		tools.design = open.ID
		if generatesImages(r) {
			tools.generated = &generatedImages{home: lp.runtimeHome(r.Engine)}
		}
	}
	return tools
}

// seatName is how a seat signs what it changes.
func seatName(r core.Role, kind string) string {
	if r.Name != "" {
		return r.Name
	}
	return kind
}

// relationsFor is what a role may link its task as: the researcher and QA
// decide what a task waits for; the others only point at related work.
func relationsFor(kind string) []string {
	if kind == core.RoleResearcher || kind == core.RoleQA {
		return []string{core.RelationDependsOn, core.RelationBlocks, core.RelationRelatesTo}
	}
	return []string{core.RelationRelatesTo}
}

// managerTools are the PM's in every turn where it decides something: its
// look at the list, where a task goes after its checks, and whether a
// change lands. It looks across the list rather than working on one task,
// and may tidy any unfinished task's title and requirements, link tasks,
// queue a split or a sibling task, and leave notes.
func (lp *Loop) managerTools(projectID string, seat core.Role) roleTools {
	return roleTools{lp: lp, projectID: projectID, by: core.LinkedByPM, name: seatName(seat, core.RolePM), kind: core.RolePM, manages: true, queued: new(int)}
}

// answerTools are the PM's when the assistant asks it something: answering
// changes nothing, so it may look tasks up and leave notes, but not edit,
// link or queue.
func (lp *Loop) answerTools(projectID string, seat core.Role) roleTools {
	r := lp.managerTools(projectID, seat)
	r.notesOnly = true
	return r
}

func (r roleTools) Handler() session.ToolHandler {
	return session.ToolHandlerFunc(func(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
		out, err := r.call(ctx, call.Name, call.Arguments)
		if err != nil {
			return session.ToolResult{Content: err.Error(), IsError: true}, nil
		}
		return session.ToolResult{Content: out}, nil
	})
}

var errNoTask = errors.New("there is no such task in this project")

func (r roleTools) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", errors.New("arguments must be an object of strings")
	}
	if !r.offers(name) {
		return "", fmt.Errorf("there is no tool %q", name)
	}
	// A role changes only its own task, and only while it is still there;
	// the PM names the task it changes.
	taskID, while := r.taskID, r.status
	if r.manages {
		taskID, while = in["task_id"], ""
	}
	switch name {
	case "list_tasks":
		return r.list(ctx, in["which"], in["related_to"], in["text"])
	case "read_task":
		return r.read(ctx, in["task_id"])
	case "read_notes":
		return r.notes(ctx, in["task_id"], in["from"])
	case "link_tasks", "unlink_tasks":
		l := core.Link{Project: r.projectID, Task: taskID, Relation: in["relation"], Other: in["other_task_id"], By: r.by, Relations: r.relations, While: while}
		if name == "unlink_tasks" {
			_, err := r.lp.UnlinkTasks(ctx, l)
			return changed("Unlinked.", err)
		}
		_, err := r.lp.LinkTasks(ctx, l)
		return changed("Linked.", err)
	case "edit_task":
		e := core.EditInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Objective: in["title"], Criteria: replacement(in["requirements"])}
		if add := strings.TrimSpace(in["add_requirement"]); add != "" {
			e.Add = []string{add}
		}
		if strings.TrimSpace(e.Objective) == "" && e.Criteria == nil && e.Add == nil {
			return "", errors.New("say what to change")
		}
		_, err := r.lp.Core.EditTask(ctx, e)
		return changed("Edited.", err)
	case "add_note":
		_, err := r.lp.Core.AddNote(ctx, core.NoteInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Text: in["text"]})
		return changed("Noted.", err)
	case "propose_run_recipe":
		recipe := core.RunRecipe{Setup: in["setup"], Start: in["start"], URL: in["url"], Ready: in["ready"]}
		_, err := r.lp.Core.ProposeRunRecipe(ctx, r.projectID, r.name, recipe, in["why"])
		return changed("Proposed. The owner decides whether QA uses it; nothing changes until they do.", err)
	case "attach_file":
		return r.attach(ctx, in["name"], in["content"], in["path"], in["generated"])
	case "queue_task":
		return r.queue(ctx, in["title"], in["requirements"], in["depends_on"])
	default:
		return "", fmt.Errorf("there is no tool %q", name)
	}
}

// offers is whether the role's turn was given the tool, which is all that
// decides whether it may call it.
func (r roleTools) offers(name string) bool {
	return slices.ContainsFunc(r.Definitions(), func(d session.ToolDefinition) bool { return d.Name == name })
}

// changed is the reply to a change: done, or why not, told without
// anything about other projects.
func changed(done string, err error) (string, error) {
	if err != nil {
		return "", hideProjects(err)
	}
	return done, nil
}

// queue asks for a new task on the PM's behalf, which waits for what the PM
// says it does.
func (r roleTools) queue(ctx context.Context, title, requirements, dependsOn string) (string, error) {
	if *r.queued >= maxPMQueued {
		return "", fmt.Errorf("you can queue at most %d tasks each time you look", maxPMQueued)
	}
	deps := strings.FieldsFunc(dependsOn, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' })
	t, err := r.lp.Core.QueueTaskAs(ctx, r.projectID, core.TaskInput{Objective: title, Criteria: lines(requirements), DependsOn: deps}, core.LinkedByPM)
	if err != nil {
		return "", hideProjects(err)
	}
	*r.queued++
	return "Queued " + t.Label() + ".", nil
}

// attach keeps a file with the design input the designer is giving: text
// it wrote out, a file in its workspace, which is only ever read, or an
// image it generated in this turn.
func (r roleTools) attach(ctx context.Context, name, content, path, generated string) (string, error) {
	var data []byte
	given := 0
	for _, s := range []string{content, strings.TrimSpace(path), strings.TrimSpace(generated)} {
		if s != "" {
			given++
		}
	}
	switch {
	case given != 1 && r.generated != nil:
		return "", errors.New("give one of content, path or generated")
	case given != 1:
		return "", errors.New("give either content or path, not both")
	case content != "":
		data = []byte(content)
	case strings.TrimSpace(generated) != "":
		var file string
		var err error
		if data, file, err = r.generated.read(generated); err != nil {
			return "", err
		}
		if strings.TrimSpace(name) == "" {
			name = file
		}
	default:
		var err error
		if data, err = workspaceFile(r.workDir, path); err != nil {
			return "", err
		}
		if strings.TrimSpace(name) == "" {
			name = filepath.Base(path)
		}
	}
	kept, err := r.lp.Core.AttachToDesign(ctx, core.DesignFiles{Project: r.projectID, Task: r.taskID, By: r.name, Kind: r.kind, While: r.status, Design: r.design, Files: []core.NewFile{{Name: name, Data: data}}})
	if err != nil {
		return "", hideProjects(err)
	}
	return fmt.Sprintf("Attached %s (%d bytes) to your design input.", kept[0].Name, kept[0].Size), nil
}

// lines is one item a line, or nil for none.
func lines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•")); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// clearAll is what a role writes as requirements to remove them all, since
// an empty value keeps them.
const clearAll = "none"

// replacement is the requirements an edit puts in place: nil keeps them,
// and an empty list, written as clearAll, removes them all.
func replacement(s string) []string {
	if strings.EqualFold(strings.TrimSpace(s), clearAll) {
		return []string{}
	}
	return lines(s)
}

// hideProjects keeps a refusal from saying anything about tasks outside
// the role's project.
func hideProjects(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return errNoTask
	}
	return err
}
