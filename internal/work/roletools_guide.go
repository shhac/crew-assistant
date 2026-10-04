package work

import (
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/roles"
)

// ownerAnswerGuide is shared by the PM and planner in their existing turn.
const ownerAnswerGuide = " When folding an owner's answer into a task, put clearly owner-performed work into edit_task.owner_checks, one quoted owner clause per line, preserving the owner's wording. Explicit undertakings such as 'After landing, I will run this', 'I will check outside the sandbox', 'I will test on another host', or 'I will do a live or visual review' are owner checks. For a mixed answer, keep the team portion in criteria and the owner portion in owner checks in one edit_task call. Preserve unrelated criteria and never change brief criteria. Prefer add_requirement to append team work. For replacements, read_task first, use its Requirements JSON array to preserve multiline criteria and literal punctuation, and supply its current text_version; reread after a stale-version refusal. Additive changes merge into current state; repeated owner checks add nothing. Ambiguous answers retain the existing requirement behavior: mentioning a sandbox, another host or a live check alone does not assign work to the owner. An owner undertaking retained in the original answer context is not a team requirement. Record new answer clauses with edit_task; plan.owner_checks remains only for moving exact existing task criteria."

const ownerEditGuide = " requirements also accepts a JSON array of strings, copied from Requirements JSON in read_task; use this lossless form for multiline criteria.  owner_checks adds quoted owner clauses, one per line or a JSON array of strings for multiline wording, or empty. read_task includes Owner checks JSON for lossless replay. text_version is the current version from read_task when replacing requirements with owner checks, or empty for additive edits. add_requirement appends team work without replacing other criteria, or empty."

// guide tells the role what its tools are for.
func (r roleTools) guide() string {
	guide := r.guideTools()
	if core.Rewrites(r.kind) && !r.notesOnly {
		guide += ownerAnswerGuide
	}
	if r.checks != nil {
		guide += " Use run_check for the project's check; repeat while it says still running. The daemon hosts its sandbox, including localhost when the project allows it, whatever your engine."
	}
	if r.lin != nil {
		guide += "\n\n" + connections.LinGuide(r.lin.writes && !r.notesOnly)
	}
	if r.proposes {
		guide += " QA can also use this project's daemon-hosted app, on this machine only, when the project has a run recipe: "
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
		return guide + fmt.Sprintf(" You also look after the tasks themselves. set_blocker adds an external condition (manual or daemon_includes); clear_blocker clears a condition the team set. Where a task's title or requirements are messy, tidy them with edit_task; every change is kept and the owner can undo it. Where one task depends on another, set that up with link_tasks, and take back a link the team set with unlink_tasks. Where a task should be split, or needs a sibling, ask for the new one with queue_task (at most %d each time you look). add_note leaves a note on a task for the team and the owner.", maxPMQueued)
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
		if r.productionTurn > 0 {
			guide += " " + roles.InlineAttachmentGuide
			guide += fmt.Sprintf(" In production, attach_file keeps at most %d asset files per turn, each up to %d MB, and only %s. Name each wanted asset with asset; use rejected: true instead for rejected variants. The daemon builds their zip archive (up to %d MB). A task can keep %d production asset files and %d MB of assets, separately from ordinary attachments. Return provenance for every delivered asset. Attachments write nothing to the repository.", core.MaxAttachmentsPerSet, core.MaxAttachmentBytes>>20, core.AttachmentKinds, core.MaxRejectedBytes>>20, core.MaxProductionAssets, core.MaxProductionBytes>>20)
		} else {
			guide += " " + roles.InlineAttachmentGuide
			guide += fmt.Sprintf(" attach_file keeps a file with the design input you are giving, such as a mockup: at most %d files of up to %d MB each, and only %s.", core.MaxAttachmentsPerSet, core.MaxAttachmentBytes>>20, core.AttachmentKinds)
		}
		if r.generated != nil {
			guide += " It can also keep an image you generated in this turn, named by its file name in generated, within the same limits. Use generated instead of inline content for large images."
		}
	}
	return guide
}

func (r roleTools) Definitions() []session.ToolDefinition {

	defs := r.definitions()
	if r.checks != nil {
		defs = append(defs, session.ToolDefinition{Name: "run_check", Description: "Run the project's check in a fresh workspace copy in a daemon-hosted sandbox. Each call waits at most 45 seconds; while still running, call again to wait for the same run. A call after its result starts a new check.", Schema: schema([]string{})})
	}
	if r.lin != nil {
		defs = append(defs, session.ToolDefinition{Name: "lin", Description: "Use this project’s linked Linear account. Give args or a shipped reference (commands or output).", Schema: map[string]any{"type": "object", "properties": map[string]any{"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "reference": map[string]any{"type": "string"}}, "required": []string{"args", "reference"}, "additionalProperties": false}})
	}
	if r.chat != nil {
		defs = append(defs, session.ToolDefinition{Name: "order_tasks", Description: "Set the entire queued to-do list in order. task_ids is every queued task id separated by commas. If the list changed, read it again and try again.", Schema: schema([]string{"task_ids"})})
	}
	if r.proposes && !r.notesOnly {
		defs = append(defs, session.ToolDefinition{Name: "propose_run_recipe", Description: "Propose how QA starts this code project's app to use it, for the owner to accept; nothing changes until they do, and one proposal waits at a time. setup runs once first, offline, since dependencies come in through the project's prepare folders; or empty. start is the command that starts the app, which reads its port from the PORT environment variable. url is where it answers: http on 127.0.0.1, localhost or [::1], with {port} as its port and nowhere else, such as http://127.0.0.1:{port}/. ready is a command that succeeds once the app is ready, or empty to wait until url answers. why is one line on what you found that makes this the way to run it.", Schema: schema([]string{"setup", "start", "url", "ready", "why"})})
	}
	if r.design != "" {
		for i := range defs {
			if defs[i].Name == "attach_file" {
				props := defs[i].Schema["properties"].(map[string]any)
				props["content"].(map[string]any)["maxLength"] = roles.MaxInlineAttachmentBytes
			}
		}
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
			session.ToolDefinition{Name: "set_blocker", Description: "Hold a task on an external condition. kind is manual or daemon_includes; description is a short condition (required for manual); other_task_id names the same project task whose landing the running daemon must include, or empty for manual; holds is start or landing. Owner conditions stay under owner control.", Schema: schema([]string{"task_id", "kind", "description", "other_task_id", "holds"})},
			session.ToolDefinition{Name: "clear_blocker", Description: "Clear an external blocker the team set, by its blocker_id.", Schema: schema([]string{"task_id", "blocker_id"})},
			session.ToolDefinition{Name: "edit_task", Description: "Tidy an unfinished task of this project. task_id is its readable or canonical id. title replaces its title, or empty to keep it. requirements replaces all its requirements, one to a line; none removes them all; empty keeps them. Every change is kept and the owner can undo it." + ownerEditGuide, Schema: schema([]string{"task_id", "title", "requirements", "add_requirement", "owner_checks", "text_version"})},
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
		edit = session.ToolDefinition{Name: "edit_task", Description: "Change your own task's title and requirements. title replaces its title, or empty to keep it. requirements replaces all its requirements, one to a line; none removes them all; empty keeps them. add_requirement adds one, or empty. Every change is kept and the owner can undo it." + ownerEditGuide, Schema: schema([]string{"title", "requirements", "add_requirement", "owner_checks", "text_version"})}
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
		attach.Description += " " + roles.InlineAttachmentGuide
		if r.generated != nil {
			attach.Description += " Use generated for large generated images."
		}
		defs = append(defs, attach)
		if r.productionTurn > 0 {
			fields := []string{"name", "content", "path", "asset", "rejected"}
			if r.generated != nil {
				fields = append(fields, "generated")
			}
			defs[len(defs)-1].Schema = schema(fields)
			defs[len(defs)-1].Description += " Production: asset names the wanted asset, or empty; rejected is true to archive a rejected variant, or empty. Give either asset or rejected."
		}
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
