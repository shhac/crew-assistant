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

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// roleTools are what a role can look up about its project while it works,
// and the changes to tasks it may make: links, its task's requirements and
// notes. Everything it may reach is captured when its turn starts, never
// taken from what the model asks for: the project, its own task, and who it
// is. Its handler runs while the turn runs and calls Core, which takes the
// store's lock; that holds only because no turn ever runs inside a store
// update.
type roleTools struct {
	lin       *linBinding
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
	chat    *pmChatChanges
	// notesOnly limits the PM to leaving notes, for a turn that answers a
	// question and so changes nothing else.
	notesOnly bool
	// design is the request the designer is answering, in a designing turn
	// only: attach_file keeps files with it, reading any it names from
	// workDir.
	design, workDir string
	productionTurn  int
	engine          string
	// generated is where a Codex designer's generated images are found this
	// turn, which attach_file may keep too; nil on an engine that can't
	// generate images.
	generated *generatedImages
	// proposes lets the researcher or the PM of a code project propose how
	// QA runs the app, for the owner to accept; run is the recipe in use.
	proposes bool
	run      *core.RunRecipe
	checks   *checkRuns
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
	tools := roleTools{engine: r.Engine, lp: lp, projectID: t.ProjectID, taskID: t.ID, status: t.Status, by: core.TeamLinker(r.Member, kind), name: seatName(r, kind), kind: kind, relations: relationsFor(kind)}
	if open := t.OpenDesign(); kind == core.RoleDesigner && t.Status == core.TaskDesigning && open != nil {
		tools.design = open.ID
		if open.Production != nil && len(open.Production.Turns) > 0 {
			tools.productionTurn = open.Production.Turns[len(open.Production.Turns)-1].N
		}
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
	r := roleTools{lp: lp, projectID: projectID, by: core.LinkedByPM, name: seatName(seat, core.RolePM), kind: core.RolePM, manages: true, queued: new(int)}
	r.lin = lp.linBinding(projectID)
	return r
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

func (r roleTools) call(ctx context.Context, name string, raw json.RawMessage) (out string, err error) {
	if r.chat != nil {
		r.chat.mu.Lock()
		defer r.chat.mu.Unlock()
		defer func() {
			if err == nil {
				r.recordChatChange(ctx, name, raw, out)
			}
		}()
	}
	return r.execute(ctx, name, raw)
}
func (r roleTools) execute(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if name == "lin" {
		return r.callLin(ctx, raw)
	}
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
	case "run_check":
		return r.checks.call(ctx)
	case "order_tasks":
		ids := strings.FieldsFunc(in["task_ids"], func(c rune) bool { return c == ',' || c == '\n' })
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		tasks, err := r.lp.Core.OrderTasks(ctx, r.projectID, ids, core.OrderedByPM)
		if err == nil && r.chat != nil {
			r.chat.items = append(r.chat.items, orderedReceipt(tasks))
		}
		return changed("Reordered to-do list.", err)
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
	case "set_blocker":
		if in["holds"] != "start" && in["holds"] != "landing" {
			return "", errors.New("holds must be start or landing")
		}
		_, err := r.lp.SetBlocker(ctx, core.BlockerInput{Project: r.projectID, Task: taskID, Kind: in["kind"], Description: in["description"], Other: in["other_task_id"], LandingOnly: in["holds"] == "landing", By: r.by})
		return changed("Blocked.", err)
	case "clear_blocker":
		_, err := r.lp.ClearBlocker(ctx, r.projectID, taskID, in["blocker_id"], r.by, "cleared by the team")
		return changed("Cleared.", err)
	case "edit_task":
		var existing []string
		if strings.TrimSpace(in["requirements"]) != "" {
			snap, err := r.lp.Core.Snapshot(ctx)
			if err != nil {
				return "", err
			}
			t, ok := findTask(snap, r.projectID, taskID)
			if !ok {
				return "", errNoTask
			}
			existing = t.Criteria
		}
		criteria, err := replacement(in["requirements"], existing)
		if err != nil {
			return "", err
		}
		e := core.EditInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Objective: in["title"], Criteria: criteria}
		if checks := in["owner_checks"]; checks != "" {
			if !core.Rewrites(r.kind) {
				return "", errors.New("only PM and researcher may add owner checks")
			}
			// Preserve quoted clauses, including punctuation and leading
			// dashes, rather than parsing them as requirement bullets.
			if jsonArrayInput(checks) {
				if err := json.Unmarshal([]byte(checks), &e.OwnerChecks); err != nil {
					return "", errors.New("owner_checks must be a JSON array of strings")
				}
			} else {
				for _, clause := range strings.Split(checks, "\n") {
					if clause = strings.TrimSpace(clause); clause != "" {
						e.OwnerChecks = append(e.OwnerChecks, clause)
					}
				}
			}
			if len(e.OwnerChecks) == 0 {
				return "", errors.New("owner_checks must contain quoted owner clauses")
			}
		}
		if version := in["text_version"]; version != "" {
			n, err := strconv.Atoi(version)
			if err != nil || n < 0 {
				return "", errors.New("text_version must be a nonnegative integer or empty")
			}
			e.TextVersion = &n
		}
		if add := strings.TrimSpace(in["add_requirement"]); add != "" {
			e.Add = []string{add}
		}
		if strings.TrimSpace(e.Objective) == "" && e.Criteria == nil && e.Add == nil && e.OwnerChecks == nil {
			return "", errors.New("say what to change")
		}
		_, err = r.lp.Core.EditTask(ctx, e)
		return changed("Edited.", err)
	case "add_note":
		_, err := r.lp.Core.AddNote(ctx, core.NoteInput{Project: r.projectID, Task: taskID, By: r.name, Kind: r.kind, While: while, Text: in["text"]})
		return changed("Noted.", err)
	case "propose_run_recipe":
		recipe := core.RunRecipe{Setup: in["setup"], Start: in["start"], URL: in["url"], Ready: in["ready"]}
		_, err := r.lp.Core.ProposeRunRecipe(ctx, r.projectID, r.name, recipe, in["why"])
		return changed("Proposed. The owner decides whether QA uses it; nothing changes until they do.", err)
	case "attach_file":
		return r.attach(ctx, in["name"], in["content"], in["path"], in["generated"], in["asset"], in["rejected"])
	case "queue_task":
		return r.queue(ctx, in["title"], in["requirements"], in["depends_on"], in["linear_issue"])
	case "link_task_linear":
		return r.linkLinear(ctx, taskID, in["kind"], in["ref"])
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
// says it does, and is linked to the Linear issue it names, if any. The issue
// is read first, so a failed read queues nothing.
func (r roleTools) queue(ctx context.Context, title, requirements, dependsOn, issue string) (string, error) {
	if *r.queued >= maxPMQueued {
		return "", fmt.Errorf("you can queue at most %d tasks each time you look", maxPMQueued)
	}
	deps := strings.FieldsFunc(dependsOn, func(c rune) bool { return c == ',' || c == ' ' || c == '\n' })
	in := core.TaskInput{Objective: title, Criteria: lines(requirements), DependsOn: deps}
	if strings.TrimSpace(issue) == "" {
		t, err := r.lp.Core.QueueTaskAs(ctx, r.projectID, in, core.LinkedByPM)
		return r.tellQueued(t, "", err)
	}
	ref, err := r.linearRef(ctx, "issue", issue)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Objective) == "" {
		in.Objective = ref.Identifier + ": " + ref.Title
	}
	t, err := r.lp.Core.QueueLinearTask(ctx, r.projectID, in, ref, core.LinkedByPM)
	return r.tellQueued(t, ref.Identifier, err)
}

// tellQueued counts a task the PM queued and tells it, and the owner's chat,
// what came of it.
func (r roleTools) tellQueued(t core.Task, issue string, err error) (string, error) {
	if err != nil {
		return "", hideProjects(err)
	}
	*r.queued++
	linked := ""
	if issue != "" {
		linked = ", linked to Linear " + issue
	}
	if r.chat != nil {
		r.chat.items = append(r.chat.items, core.PMChatChange{Kind: "queued", Summary: "Queued “" + t.Objective + "”" + linked, Tasks: []string{t.ID}})
	}
	return "Queued " + t.Label() + linked + ".", nil
}

// linkLinear links one of the project's tasks to a Linear issue or project
// the PM names, read through the project's own Linear account.
func (r roleTools) linkLinear(ctx context.Context, taskID, kind, input string) (string, error) {
	if kind != "issue" && kind != "project" {
		return "", errors.New("kind is issue or project")
	}
	ref, err := r.linearRef(ctx, kind, input)
	if err != nil {
		return "", err
	}
	t, err := r.lp.Core.AddTaskLinear(ctx, r.projectID, taskID, ref, core.LinkedByPM)
	if err != nil {
		return "", hideProjects(err)
	}
	if r.chat != nil {
		r.chat.items = append(r.chat.items, core.PMChatChange{Kind: "linked", Summary: "Linked “" + t.Objective + "” to Linear " + ref.Identifier, Tasks: []string{t.ID}})
	}
	return "Linked " + t.Label() + " to Linear " + kind + " " + ref.Identifier + ".", nil
}

// attach keeps a file with the design input the designer is giving: text
// it wrote out, a file in its workspace, which is only ever read, or an
// image it generated in this turn.
func (r roleTools) attach(ctx context.Context, name, content, path, generated string, production ...string) (string, error) {
	if len(content) > roles.MaxInlineAttachmentBytes || content == roles.OversizedInlineAttachment {
		return "", errors.New(roles.InlineAttachmentGuide)
	}
	asset, rejected := "", ""
	if len(production) == 2 {
		asset, rejected = production[0], production[1]
	}
	if r.productionTurn == 0 && (asset != "" || rejected != "") {
		return "", errors.New("asset and rejected are only offered for production requests")
	}
	if r.productionTurn > 0 && ((asset == "") == (rejected == "")) {
		return "", errors.New("give either a wanted asset name or rejected: true")
	}
	if rejected != "" && rejected != "true" {
		return "", errors.New("rejected must be true or empty")
	}
	var data []byte
	made := "from the workspace"
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
		made = "written out by " + r.name
	case strings.TrimSpace(generated) != "":
		made = "image generation (" + config.EngineLabel(r.engine) + ")"
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
	in := core.DesignFiles{Project: r.projectID, Task: r.taskID, By: r.name, Kind: r.kind, While: r.status, Design: r.design, Files: []core.NewFile{{Name: name, Made: made, Data: data}}}
	var kept []core.Attachment
	var err error
	switch {
	case rejected != "":
		var archive core.Attachment
		archive, err = r.lp.Core.AddRejected(ctx, in, r.productionTurn)
		kept = []core.Attachment{archive}
	case asset != "":
		kept, err = r.lp.Core.AttachAsset(ctx, in, asset, r.productionTurn)
	default:
		kept, err = r.lp.Core.AttachToDesign(ctx, in)
	}
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
func replacement(s string, existing []string) ([]string, error) {
	if strings.EqualFold(strings.TrimSpace(s), clearAll) {
		return []string{}, nil
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	// A leading bracket can be ordinary text such as [Linux]. Only claim
	// JSON-looking input, retaining the documented one-per-line format.
	trimmed := strings.TrimSpace(s)
	firstLine := strings.TrimSpace(strings.SplitN(trimmed, "\n", 2)[0])
	if jsonArrayInput(s) && !slices.Contains(existing, firstLine) {
		var out []string
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, errors.New("requirements must be a JSON array of strings")
		}
		return out, nil
	}
	if slices.ContainsFunc(existing, func(c string) bool { return strings.Contains(c, "\n") }) {
		return nil, errors.New("this task has multiline criteria; use Requirements JSON from read_task for a lossless replacement")
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if slices.Contains(existing, line) {
				// Literal stored options and punctuation are not bullets.
				out = append(out, line)
			} else {
				// Keep accepting ordinary bulleted team instructions.
				out = append(out, lines(line)...)
			}
		}
	}
	return out, nil
}

func jsonArrayInput(s string) bool {
	s = strings.TrimSpace(s)
	// Claim only a complete JSON value. An array followed by prose is a
	// literal clause in the documented one-per-line format, even when new.
	return strings.HasPrefix(s, "[") && json.Valid([]byte(s))
}

// hideProjects keeps a refusal from saying anything about tasks outside
// the role's project.
func hideProjects(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return errNoTask
	}
	return err
}
