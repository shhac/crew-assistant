package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// roleTools are what a role can look up about its project while it works,
// and the links between tasks it may make. Everything it may reach is
// captured when its turn starts, never taken from what the model asks for:
// the project, its own task, and who it is. Its handler runs while the turn
// runs and calls Core, which takes the store's lock; that holds only because
// no turn ever runs inside a store update.
type roleTools struct {
	lp        *Loop
	projectID string
	taskID    string
	// status is the task's status when the turn started. A link is made
	// only while the task is still there, since a turn the owner stopped
	// runs on but must change nothing; core checks it in the same change.
	status string
	// by is how its links are marked.
	by string
	// relations are the links it may make from its own task: the
	// researcher decides what a task waits for; the others only point at
	// related work.
	relations []string
}

func (lp *Loop) toolsFor(t core.Task, kind, memberID string) roleTools {
	return roleTools{lp: lp, projectID: t.ProjectID, taskID: t.ID, status: t.Status, by: core.TeamLinker(memberID, kind), relations: relationsFor(kind)}
}

// relationsFor is what a role may link its task as: the researcher decides
// what a task waits for; the others only point at related work.
func relationsFor(kind string) []string {
	if kind == core.RoleResearcher {
		return []string{core.RelationDependsOn, core.RelationBlocks, core.RelationRelatesTo}
	}
	return []string{core.RelationRelatesTo}
}

// projectTools are the PM's: it looks across the list rather than working
// on one task, and changes links only through its answer.
func (lp *Loop) projectTools(projectID string) roleTools {
	return roleTools{lp: lp, projectID: projectID}
}

// guide tells the role what its tools are for.
func (r roleTools) guide() string {
	guide := "While you work you can look up this project's other tasks: list_tasks lists them, filtered by which, related_to or text, and read_task reads one, with its plan, links and latest reviews. Look up the ones that bear on yours rather than guessing what they change."
	switch {
	case slices.Contains(r.relations, core.RelationDependsOn):
		guide += " With link_tasks you can say your task depends on another, blocks another, or relates to one; unlink_tasks takes back a link your team set."
	case len(r.relations) > 0:
		guide += " With link_tasks you can mark another task as related to yours, so whoever works on either knows to look; unlink_tasks takes it back."
	}
	return guide
}

func (r roleTools) Definitions() []session.ToolDefinition {
	defs := []session.ToolDefinition{
		{Name: "list_tasks", Description: "List this project's tasks, one line each with id, status and how it links to others. which is unfinished (the default when empty), finished or all. related_to is a task id to list only the tasks linked to it, or empty. text keeps only tasks whose objective contains it, or empty.", Schema: schema([]string{"which", "related_to", "text"})},
		{Name: "read_task", Description: "Read one of this project's tasks: what it is for, its plan, where it is, its links and its latest draft and reviews. Use it for the tasks that bear on yours.", Schema: schema([]string{"task_id"})},
	}
	if len(r.relations) == 0 {
		return defs
	}
	return append(defs,
		session.ToolDefinition{Name: "link_tasks", Description: "Link your own task to another of this project's tasks. relation is " + relationGuide(r.relations) + " A pair has one link; the owner's links stay as they are.", Schema: schema([]string{"relation", "other_task_id"})},
		session.ToolDefinition{Name: "unlink_tasks", Description: "Take away a link between your own task and another that your team set; links the owner or assistant set stay.", Schema: schema([]string{"other_task_id"})},
	)
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
	case "link_tasks", "unlink_tasks":
		if len(r.relations) == 0 {
			return "", errors.New("you cannot change links")
		}
		l := core.Link{Project: r.projectID, Task: r.taskID, Relation: in["relation"], Other: in["other_task_id"], By: r.by, Relations: r.relations, While: r.status}
		change, done := r.lp.LinkTasks, "Linked."
		if name == "unlink_tasks" {
			change, done = r.lp.UnlinkTasks, "Unlinked."
		}
		if _, err := change(ctx, l); err != nil {
			return "", hideProjects(err)
		}
		return done, nil
	}
	return "", fmt.Errorf("there is no tool %q", name)
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
		fmt.Fprintf(&b, "- %s (%s)%s: %s%s\n", t.ID, t.Status, mine, text.Clip(t.Objective, 200), linksLine(t))
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

func linksLine(t core.Task) string {
	var parts []string
	for _, l := range linkGroups(t) {
		if len(l.ids) > 0 {
			parts = append(parts, l.name+" "+strings.Join(l.ids, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, "; ") + "]"
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
	fmt.Fprintf(&b, "%s (%s, %s): %s\n", t.ID, t.Status, t.Stage, text.Clip(t.Objective, 600))
	for _, c := range t.Criteria {
		fmt.Fprintf(&b, "- criterion: %s\n", text.Clip(c, 300))
	}
	for _, l := range linkGroups(t) {
		for _, other := range l.ids {
			if o, ok := findTask(snap, r.projectID, other); ok {
				fmt.Fprintf(&b, "- %s %s (%s): %s\n", l.name, o.ID, o.Status, text.Clip(o.Objective, 200))
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
	if n := len(t.Revisions); n > 0 {
		last := t.Revisions[n-1]
		fmt.Fprintf(&b, "Latest draft %d: %s\n", last.N, text.Clip(last.Summary, 800))
		for _, v := range t.Verdicts {
			if v.Revision == last.N {
				fmt.Fprintf(&b, "- %s: %s %s\n", v.Role, v.Outcome, text.Clip(v.Summary, 300))
			}
		}
	}
	if t.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", t.Branch)
	}
	return b.String(), nil
}
