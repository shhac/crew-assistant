package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
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

// guide tells the role what its tools are for.
func (r roleTools) guide() string {
	guide := r.guideTools()
	if r.proposes {
		guide += " QA can also start this project's app and use it, on this machine only, when the project has a run recipe: "
		if r.run != nil {
			guide += "it has one now (start: " + r.run.Start + "; URL: " + r.run.URL + ")."
		} else {
			guide += "it has none yet."
		}
		guide += " If you have found out how the app starts and a better recipe would help QA, propose one with propose_run_recipe; the owner decides whether it is used. Propose only what the repository shows works, and don't propose one without cause."
	}
	return guide
}

func (r roleTools) guideTools() string {
	guide := "While you work you can look up this project's other tasks: list_tasks lists them, filtered by which, related_to or text, and read_task reads one, with its plan, links, latest notes and latest reviews; read_notes reads all of a task's notes, a page at a time. Look up the ones that bear on yours rather than guessing what they change."
	if r.notesOnly {
		return guide + " add_note leaves a note on a task for the team and the owner. Answering changes nothing else, so say what you would change instead of changing it."
	}
	if r.manages {
		return guide + fmt.Sprintf(" You also look after the tasks themselves. Where a task's title or requirements are messy, tidy them with edit_task; every change is kept and the owner can undo it. Where one task depends on another, set that up with link_tasks, and take back a link the team set with unlink_tasks. Where a task should be split, or needs a sibling, ask for the new one with queue_task (at most %d each time you look). add_note leaves a note on a task for the team and the owner.", maxPMQueued)
	}
	if r.taskID == "" {
		return guide
	}
	switch {
	case slices.Contains(r.relations, core.RelationDependsOn):
		guide += " With link_tasks you can say your task depends on another, blocks another, or relates to one; unlink_tasks takes back a link your team set. Where there is a dependency, set it up rather than leave it to be found later."
	case len(r.relations) > 0:
		guide += " With link_tasks you can mark another task as related to yours, so whoever works on either knows to look; unlink_tasks takes it back."
	}
	switch r.kind {
	case core.RoleResearcher:
		guide += " With edit_task you can reword your task's title and requirements, or remove ones that no longer apply, as your findings show they should read; your plan stays the main record of what you found. Every change is kept and the owner can undo it."
	case core.RoleReviewer, core.RoleQA:
		guide += " If you find a gap in your task's requirements, add the missing one with edit_task."
	default:
		guide += " If your task is missing a requirement, add it with edit_task."
	}
	guide += " add_note leaves a note on your task for the rest of the team and the owner, such as something whoever works on it next should know; it sits beside your reply and never replaces it."
	if r.design != "" {
		guide += fmt.Sprintf(" attach_file keeps a file with the design input you are giving, such as a mockup: at most %d files of up to %d MB each, and only %s.", core.MaxAttachmentsPerSet, core.MaxAttachmentBytes>>20, core.AttachmentKinds)
	}
	return guide
}

func (r roleTools) Definitions() []session.ToolDefinition {
	defs := r.definitions()
	if r.proposes && !r.notesOnly {
		defs = append(defs, session.ToolDefinition{Name: "propose_run_recipe", Description: "Propose how QA starts this code project's app to use it, for the owner to accept; nothing changes until they do, and one proposal waits at a time. setup runs once first, offline, since dependencies come in through the project's prepare folders; or empty. start is the command that starts the app, which reads its port from the PORT environment variable. url is where it answers: http on 127.0.0.1, localhost or [::1], with {port} as its port and nowhere else, such as http://127.0.0.1:{port}/. ready is a command that succeeds once the app is ready, or empty to wait until url answers. why is one line on what you found that makes this the way to run it.", Schema: schema([]string{"setup", "start", "url", "ready", "why"})})
	}
	return defs
}

func (r roleTools) definitions() []session.ToolDefinition {
	defs := []session.ToolDefinition{
		{Name: "list_tasks", Description: "List this project's tasks, one line each with its readable id (such as CA-12) and canonical id, status and how it links to others. which is unfinished (the default when empty), finished or all. related_to is a task id, readable or canonical, to list only the tasks linked to it, or empty. text keeps only tasks whose objective contains it, or empty.", Schema: schema([]string{"which", "related_to", "text"})},
		{Name: "read_task", Description: "Read one of this project's tasks: what it is for, its plan, where it is, its links, notes and its latest draft and reviews. task_id is its readable id (such as CA-12) or its canonical id. Use it for the tasks that bear on yours.", Schema: schema([]string{"task_id"})},
		{Name: "read_notes", Description: fmt.Sprintf("Read a task's notes, numbered from 1, oldest first, up to %d at a time. task_id is its readable or canonical id. from is the number of the first note to read, or empty for the latest.", maxNotesPage), Schema: schema([]string{"task_id", "from"})},
	}
	note := session.ToolDefinition{Name: "add_note", Description: "Leave a note on one of this project's tasks, for the team and the owner to read. task_id is its readable or canonical id.", Schema: schema([]string{"task_id", "text"})}
	if r.notesOnly {
		return append(defs, note)
	}
	if r.manages {
		return append(defs,
			session.ToolDefinition{Name: "edit_task", Description: "Tidy an unfinished task of this project. task_id is its readable or canonical id. title replaces its title, or empty to keep it. requirements replaces all its requirements, one to a line; none removes them all; empty keeps them. Every change is kept and the owner can undo it.", Schema: schema([]string{"task_id", "title", "requirements"})},
			session.ToolDefinition{Name: "link_tasks", Description: "Link two of this project's tasks. relation is what task_id is to other_task_id: " + relationGuide([]string{core.RelationDependsOn, core.RelationBlocks, core.RelationRelatesTo}) + " A pair has one link; the owner's links stay as they are, and a task whose work has begun can't be made to wait.", Schema: schema([]string{"task_id", "relation", "other_task_id"})},
			session.ToolDefinition{Name: "unlink_tasks", Description: "Take away a link between two of this project's tasks that the team set; links the owner or assistant set stay.", Schema: schema([]string{"task_id", "other_task_id"})},
			session.ToolDefinition{Name: "queue_task", Description: "Ask for a new task in this project, such as part of a task split off or a sibling task. title is what it is for; requirements are its requirements, one to a line, or empty. depends_on is the ids of tasks it must wait for, separated by commas, or empty.", Schema: schema([]string{"title", "requirements", "depends_on"})},
			note,
		)
	}
	if r.taskID == "" {
		return defs
	}
	if len(r.relations) > 0 {
		defs = append(defs,
			session.ToolDefinition{Name: "link_tasks", Description: "Link your own task to another of this project's tasks. relation is " + relationGuide(r.relations) + " other_task_id is its readable or canonical id. A pair has one link; the owner's links stay as they are.", Schema: schema([]string{"relation", "other_task_id"})},
			session.ToolDefinition{Name: "unlink_tasks", Description: "Take away a link between your own task and another that your team set; links the owner or assistant set stay. other_task_id is its readable or canonical id.", Schema: schema([]string{"other_task_id"})},
		)
	}
	edit := session.ToolDefinition{Name: "edit_task", Description: "Add a requirement your own task is missing. add_requirement is the requirement, in one line.", Schema: schema([]string{"add_requirement"})}
	if core.Rewrites(r.kind) {
		edit = session.ToolDefinition{Name: "edit_task", Description: "Change your own task's title and requirements. title replaces its title, or empty to keep it. requirements replaces all its requirements, one to a line; none removes them all; empty keeps them. add_requirement adds one, or empty. Every change is kept and the owner can undo it.", Schema: schema([]string{"title", "requirements", "add_requirement"})}
	}
	defs = append(defs, edit,
		session.ToolDefinition{Name: "add_note", Description: "Leave a note on your own task for the rest of the team and the owner to read.", Schema: schema([]string{"text"})},
	)
	if r.design != "" {
		defs = append(defs, session.ToolDefinition{Name: "attach_file", Description: "Attach a file, such as a mockup, to the design input you are giving; everyone who works on the task afterwards can open it. Give either content or path, not both. name is the file's name, with an extension saying its type (.svg, .html, .md, .txt, .json, .csv, .png, .jpg, .gif, .webp or .pdf), or empty to use the path's. content is the whole text of a text file you write out, such as an SVG or HTML mockup, or empty. path names a file already in the current directory, relative to it, or empty.", Schema: schema([]string{"name", "content", "path"})})
	}
	return defs
}

// relationGuide says what each relation a role may make means.
func relationGuide(relations []string) string {
	meanings := map[string]string{
		core.RelationDependsOn: "depends_on (yours cannot start or land before the other finishes)",
		core.RelationBlocks:    "blocks (the other cannot start or land before yours finishes)",
		core.RelationRelatesTo: "relates_to (worth reading together; nothing waits)",
	}
	parts := make([]string, len(relations))
	for i, r := range relations {
		parts[i] = meanings[r]
	}
	return strings.Join(parts, ", or ") + "."
}

// schema is a strict object of required strings, as the assistant's own
// tools are: an empty string means none.
func schema(fields []string) map[string]any {
	props := map[string]any{}
	for _, f := range fields {
		props[f] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": props, "required": fields, "additionalProperties": false}
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
	switch name {
	case "list_tasks":
		return r.list(ctx, in["which"], in["related_to"], in["text"])
	case "read_task":
		return r.read(ctx, in["task_id"])
	case "read_notes":
		return r.notes(ctx, in["task_id"], in["from"])
	}
	if (!r.manages && r.taskID == "") || (r.notesOnly && name != "add_note") {
		return "", fmt.Errorf("there is no tool %q", name)
	}
	// A role changes only its own task, and only while it is still there;
	// the PM names the task it changes.
	taskID, while := r.taskID, r.status
	if r.manages {
		taskID, while = in["task_id"], ""
	}
	var err error
	done := "Done."
	switch name {
	case "link_tasks", "unlink_tasks":
		if !r.manages && len(r.relations) == 0 {
			return "", errors.New("you cannot change links")
		}
		l := core.Link{Project: r.projectID, Task: taskID, Relation: in["relation"], Other: in["other_task_id"], By: r.by, Relations: r.relations, While: while}
		change := r.lp.LinkTasks
		done = "Linked."
		if name == "unlink_tasks" {
			change, done = r.lp.UnlinkTasks, "Unlinked."
		}
		_, err = change(ctx, l)
	case "edit_task":
		e := core.EditInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Objective: in["title"], Criteria: replacement(in["requirements"])}
		if add := strings.TrimSpace(in["add_requirement"]); add != "" {
			e.Add = []string{add}
		}
		if strings.TrimSpace(e.Objective) == "" && e.Criteria == nil && e.Add == nil {
			return "", errors.New("say what to change")
		}
		_, err = r.lp.Core.EditTask(ctx, e)
		done = "Edited."
	case "add_note":
		_, err = r.lp.Core.AddNote(ctx, core.NoteInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Text: in["text"]})
		done = "Noted."
	case "propose_run_recipe":
		if !r.proposes {
			return "", fmt.Errorf("there is no tool %q", name)
		}
		recipe := core.RunRecipe{Setup: in["setup"], Start: in["start"], URL: in["url"], Ready: in["ready"]}
		if _, err = r.lp.Core.ProposeRunRecipe(ctx, r.projectID, r.name, recipe, in["why"]); err != nil {
			return "", hideProjects(err)
		}
		return "Proposed. The owner decides whether QA uses it; nothing changes until they do.", nil
	case "attach_file":
		if r.design == "" {
			return "", fmt.Errorf("there is no tool %q", name)
		}
		return r.attach(ctx, in["name"], in["content"], in["path"])
	case "queue_task":
		if !r.manages {
			return "", fmt.Errorf("there is no tool %q", name)
		}
		return r.queue(ctx, in["title"], in["requirements"], in["depends_on"])
	default:
		return "", fmt.Errorf("there is no tool %q", name)
	}
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
// it wrote out, or a file in its workspace, which is only ever read.
func (r roleTools) attach(ctx context.Context, name, content, path string) (string, error) {
	var data []byte
	switch hasContent, hasPath := content != "", strings.TrimSpace(path) != ""; {
	case hasContent == hasPath:
		return "", errors.New("give either content or path, not both")
	case hasContent:
		data = []byte(content)
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

// designFiles lists the files attached to a design, as read_task shows
// them: on the role's own task, where each is kept, which its turn can
// read; on another task, only their names, since its turn can't open them.
func (r roleTools) designFiles(t core.Task, d core.DesignRequest) string {
	var b strings.Builder
	dir := r.lp.Core.AttachmentsDirectory(t.ID)
	for _, a := range t.Attachments {
		switch {
		case a.Design != d.ID:
		case t.ID == r.taskID:
			fmt.Fprintf(&b, "- design %d file, which you can open: %s (%s)\n", d.N, filepath.Join(dir, a.ID), a.Name)
		default:
			fmt.Fprintf(&b, "- design %d file: %s (on another task, so not readable from your turn)\n", d.N, a.Name)
		}
	}
	return b.String()
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

func (r roleTools) list(ctx context.Context, which, relatedTo, contains string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	var related core.Task
	if relatedTo != "" {
		var ok bool
		if related, ok = findTask(snap, r.projectID, relatedTo); !ok {
			return "", errNoTask
		}
	}
	var b strings.Builder
	for _, t := range snap.Tasks {
		switch {
		case t.ProjectID != r.projectID:
			continue
		case (which == "" || which == "unfinished") && t.Finished():
			continue
		case which == "finished" && !t.Finished():
			continue
		case relatedTo != "" && !linkedTo(related, t):
			continue
		case contains != "" && !strings.Contains(strings.ToLower(t.Objective), strings.ToLower(contains)):
			continue
		}
		mine := ""
		if t.ID == r.taskID {
			mine = " (your task)"
		}
		fmt.Fprintf(&b, "- %s (%s)%s: %s%s\n", t.Label(), t.Status, mine, text.Clip(t.Objective, 200), linksLine(snap, t))
	}
	if b.Len() == 0 {
		return "No tasks match.", nil
	}
	return b.String(), nil
}

// linkGroup is one kind of link a task has, with the tasks at the other end.
type linkGroup struct {
	name string
	ids  []string
}

func linkGroups(t core.Task) []linkGroup {
	return []linkGroup{{"depends on", t.DependsOn}, {"blocks", t.Blocks}, {"relates to", t.RelatesTo}}
}

func linkedTo(a, b core.Task) bool {
	return slices.ContainsFunc(linkGroups(a), func(g linkGroup) bool { return slices.Contains(g.ids, b.ID) })
}

func linksLine(snap core.Snapshot, t core.Task) string {
	var parts []string
	for _, l := range linkGroups(t) {
		if len(l.ids) > 0 {
			names := make([]string, len(l.ids))
			for i, id := range l.ids {
				names[i] = id
				if o, ok := snap.FindTask(id); ok {
					names[i] = o.Label()
				}
			}
			parts = append(parts, l.name+" "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, "; ") + "]"
}

// notes reads a page of a task's notes: from the one numbered from, or the
// latest.
func (r roleTools) notes(ctx context.Context, id, from string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	t, ok := findTask(snap, r.projectID, id)
	if !ok {
		return "", errNoTask
	}
	first := len(t.Notes) - maxNotesPage + 1
	if from = strings.TrimSpace(from); from != "" {
		if first, err = strconv.Atoi(from); err != nil || first < 1 {
			return "", errors.New("from is the number of a note, such as 1, or empty for the latest")
		}
	}
	return notesPage(t, first, maxNotesPage), nil
}

func (r roleTools) read(ctx context.Context, id string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	t, ok := findTask(snap, r.projectID, id)
	if !ok {
		return "", errNoTask
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s): %s\n", t.Label(), t.Status, t.Stage, text.Clip(t.Objective, 600))
	for _, c := range t.Criteria {
		fmt.Fprintf(&b, "- criterion: %s\n", text.Clip(c, 300))
	}
	if n := len(t.Edits); n > 0 {
		fmt.Fprintf(&b, "- title or requirements changed %d times, last by %s\n", n, t.Edits[n-1].By)
	}
	for _, l := range linkGroups(t) {
		for _, other := range l.ids {
			if o, ok := findTask(snap, r.projectID, other); ok {
				fmt.Fprintf(&b, "- %s %s (%s): %s\n", l.name, o.Label(), o.Status, text.Clip(o.Objective, 200))
			}
		}
	}
	if t.Plan != nil {
		fmt.Fprintf(&b, "Plan: %s\n", text.Clip(t.Plan.Summary, 800))
		for _, c := range t.Plan.Changes {
			fmt.Fprintf(&b, "- change: %s\n", text.Clip(c, 300))
		}
		for _, o := range t.Plan.OutOfScope {
			fmt.Fprintf(&b, "- out of scope: %s\n", text.Clip(o, 300))
		}
	}
	for _, q := range t.Research {
		fmt.Fprintf(&b, "- %s asked for more research on draft %d: %s\n", q.From, q.Revision, text.Clip(q.Question, 300))
	}
	if current, ok := t.CurrentDesignInput(); ok {
		fmt.Fprintf(&b, "Current design, the target: design %d by %s: %s\n", current.N, current.Designer, text.Clip(current.Input, 800))
		b.WriteString(r.designFiles(t, current))
	}
	var superseded []string
	for _, d := range t.Design {
		if d.Marked && d.ID != t.CurrentDesign {
			superseded = append(superseded, strconv.Itoa(d.N))
		}
	}
	if len(superseded) > 0 {
		fmt.Fprintf(&b, "- superseded designs, not the target: %s\n", strings.Join(superseded, ", "))
	}
	if n := len(t.Attachments); n > 0 {
		fmt.Fprintf(&b, "Attachments: %d\n", n)
	}
	if n := len(t.Revisions); n > 0 {
		last := t.Revisions[n-1]
		fmt.Fprintf(&b, "Latest draft %d: %s\n", last.N, text.Clip(last.Summary, 800))
		for _, v := range t.Verdicts {
			if v.Revision != last.N || v.Answered {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s%s %s\n", v.Role, v.Outcome, checkedRef(v, last), text.Clip(v.Summary, 300))
			b.WriteString(evidenceText(t, v, "  ", 300))
			if v.Next != "" || v.Note != "" {
				fmt.Fprintf(&b, "  recommends %s: %s\n", orDash(v.Next), orDash(v.Note))
			}
		}
	}
	if len(t.Notes) > 0 {
		b.WriteString("Notes:\n" + latestNotes(t))
	}
	if t.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", t.Branch)
	}
	return b.String(), nil
}
