package work

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

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
		if r.generated != nil {
			guide += " It can also keep an image you generated in this turn, named by its file name in generated, within the same limits."
		}
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
		attach := session.ToolDefinition{Name: "attach_file", Description: "Attach a file, such as a mockup, to the design input you are giving; everyone who works on the task afterwards can open it. Give either content or path, not both. name is the file's name, with an extension saying its type (.svg, .html, .md, .txt, .json, .csv, .png, .jpg, .gif, .webp or .pdf), or empty to use the path's. content is the whole text of a text file you write out, such as an SVG or HTML mockup, or empty. path names a file already in the current directory, relative to it, or empty.", Schema: schema([]string{"name", "content", "path"})}
		if r.generated != nil {
			attach.Description = "Attach a file, such as a mockup, to the design input you are giving; everyone who works on the task afterwards can open it. Give one of content, path or generated. name is the file's name, with an extension saying its type (.svg, .html, .md, .txt, .json, .csv, .png, .jpg, .gif, .webp or .pdf), or empty to use the path's or the generated image's. content is the whole text of a text file you write out, such as an SVG or HTML mockup, or empty. path names a file already in the current directory, relative to it, or empty. generated is the file name of an image you generated in this turn with your image generation tool, or empty."
			attach.Schema = schema([]string{"name", "content", "path", "generated"})
		}
		defs = append(defs, attach)
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
