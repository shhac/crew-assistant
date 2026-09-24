package engine

import "github.com/shhac/lib-agent-harness/completion"

// Tool argument types are shared with the application's strict, policy-checking bridge.
type CreateProjectArgs struct {
	Directories []string `json:"directories"`
	Title       string   `json:"title"`
	Goal        string   `json:"goal"`
	Audience    string   `json:"audience"`
	Constraints string   `json:"constraints"`
	Criteria    []string `json:"criteria"`
	Template    string   `json:"template"`
}
type UpdateBriefArgs struct {
	ProjectID   string   `json:"project_id"`
	Goal        string   `json:"goal"`
	Audience    string   `json:"audience"`
	Constraints string   `json:"constraints"`
	Criteria    []string `json:"criteria"`
}
type SetTeamArgs struct {
	ProjectID      string `json:"project_id"`
	Template       string `json:"template"`
	WriterEngine   string `json:"writer_engine"`
	ReviewerEngine string `json:"reviewer_engine"`
	MaxRounds      string `json:"max_rounds"`
	DeliverTo      string `json:"deliver_to"`
	// Code teams only.
	Repo         string   `json:"repo"`
	BranchPrefix string   `json:"branch_prefix"`
	Check        string   `json:"check"`
	Prepare      []string `json:"prepare"`
}
type SetLandingArgs struct {
	ProjectID string `json:"project_id"`
	Means     string `json:"means"`
	Via       string `json:"via"`
	Target    string `json:"target"`
	Method    string `json:"method"`
	GitHub    string `json:"github"`
	Approve   string `json:"approve"`
}
type LandTaskArgs struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
}
type WakeArgs struct {
	On        string `json:"on"`
	ProjectID string `json:"project_id"`
	Target    string `json:"target"`
	Match     string `json:"match"`
	Prompt    string `json:"prompt"`
	Timeout   string `json:"timeout"`
}
type WakeHandleArgs struct {
	Handle string `json:"handle"`
}
type QueueTaskArgs struct {
	ProjectID string   `json:"project_id"`
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
}
type StopTaskArgs struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
}
type ResolveDecisionArgs struct {
	DecisionID string `json:"decision_id"`
	Answer     string `json:"answer"`
}

type DecisionArgs struct {
	ProjectID      string   `json:"project_id"`
	Question       string   `json:"question"`
	Recommendation string   `json:"recommendation"`
	Options        []string `json:"options"`
	Why            string   `json:"why"`
	Evidence       []string `json:"evidence"`
}
type PreferenceArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type StatusArgs struct {
	ProjectID string   `json:"project_id"`
	Summary   string   `json:"summary"`
	Evidence  []string `json:"evidence"`
}

type Tool = completion.Tool
type Function = completion.Function

func Tools() []Tool {
	return []Tool{
		tool("list_connections", "List available optional resources and approved credential profiles. Availability does not make an account relevant to a project. Credentials are never exposed.", nil, nil),
		tool("query_connection", "Read through an optional integration and approved profile only when relevant to the owner request or established project context. Do not query a work account for a personal project unless the owner explicitly links it or asks. This does not grant writes, deployment, production-data access, or purchases. Slack messages require an existing C/G/D channel ID; URLs and user targets are unavailable. Use an empty profile for the Notion CLI default; other integrations require an explicitly configured profile alias. Use empty strings for other fields not required by the operation.", []string{"connection_id", "profile", "operation", "query", "resource_id"}, nil),
		tool("read_state", "Read current projects with their briefs and teams, tasks with their revisions and reviews, decisions, preferences and recent activity.", nil, nil),
		tool("create_project", "Start tracking a project of any kind — writing, email, research, code — in local state. A brief (goal, audience, constraints, criteria) says what it is for; leave fields empty when unknown. template \"draft\" gives it a writer and a reviewer for written work; empty sets no team yet. Directories are optional existing absolute paths; linking them grants nothing. No Linear issue, external tracker, or connection is required.", []string{"title", "goal", "audience", "constraints", "template"}, []string{"criteria"}),
		tool("update_brief", "Replace a project's brief with a new version: its goal, audience, constraints and criteria. Work already done is re-checked against the new version before delivery.", []string{"project_id", "goal", "audience", "constraints"}, []string{"criteria"}),
		tool("set_team", "Choose how a project's work gets done. template \"draft\" is a writer and a reviewer for written work. template \"code\" is for a project with a linked git repository: an implementer works in a private clone, a reviewer reads the change, QA runs the check command, and approval creates a local branch in the repository; nothing is pushed. writer_engine and reviewer_engine are codex or claude, or empty for the template's choice. max_rounds is how many revise-and-check rounds to try before bringing the owner a decision, or empty for the default. deliver_to is an optional absolute folder approved drafts are copied to (written work). For code: repo is the linked repository (empty for the project's first folder), branch_prefix names delivered branches (empty for crew/), check is the command QA runs (such as make check), and prepare lists ignored dependency folders to copy into the clone (such as node_modules paths). Use empty values for settings that do not apply. Tasks already under way keep their team.", []string{"project_id", "template", "writer_engine", "reviewer_engine", "max_rounds", "deliver_to", "repo", "branch_prefix", "check"}, []string{"prepare"}),
		tool("set_landing", "Set what landing an approved change means for a code project; tasks already under way keep what they started with. means is the owner's own words for it. via is branch (approval creates a new local branch; nothing moves), push (approval fast-forwards target, such as main, in the owner's repository: never forced, so work already there is never replaced; a checked-out target is updated only if the owner's repository allows it and the checkout is clean), or pull-request (not built yet). target is the branch to land on for push, empty for branch. method is fast-forward or empty. github is empty. approve is before (the owner approves each change before it lands) or none (a change that passes its checks lands without asking); use none only when the owner has said so.", []string{"project_id", "means", "via", "target", "method", "github", "approve"}, nil),
		tool("land_task", "Land a change that was delivered earlier, such as a branch, under the project's current landing policy. Its earlier approval stands; it catches up with the target first, and QA checks the merged result. A change built on another that has not landed is refused: land that one first. Use it only when the owner has asked for this change to land in this conversation or a wake-up continuation they set up.", []string{"project_id", "task_id"}, nil),
		tool("wake_me_when", "Be woken later, in a new turn, when something changes, instead of checking back. on is task (target is a task id; match is a status to wait for, such as landed, or empty for any change), branch (project_id and a branch name in its repository; fires when the tip moves), or time (target is RFC 3339 or a duration such as 30m). prompt is your own continuation: what to do when woken. timeout is a duration or empty for a day, at most 7 days; a wake that times out is still delivered, saying so. Returns a handle you can cancel. Several can wait at once. When woken you get the handle, when it was registered, seen and delivered, and what changed; check the current state before acting if much time has passed.", []string{"on", "project_id", "target", "match", "prompt", "timeout"}, nil),
		tool("list_wakes", "List wake-ups still waiting or about to be delivered, with their handles.", nil, nil),
		tool("cancel_wake", "Cancel a wake-up by its handle when it is no longer needed.", []string{"handle"}, nil),
		tool("queue_task", "Ask the project's team for one outcome. The writer drafts it, reviewers check it against the brief, and the owner approves delivery. Criteria are specific to this task and add to the brief's.", []string{"project_id", "objective"}, []string{"criteria"}),
		tool("stop_task", "Stop a queued or running task when the owner asks. A turn already under way finishes but changes nothing; any decision it was waiting on is closed.", []string{"project_id", "task_id"}, nil),
		tool("resolve_decision", "Answer an open decision with the owner's choice, in their words. Use it only when the owner has just given that answer in this conversation.", []string{"decision_id", "answer"}, nil),
		tool("ask_decision", "Prepare an unresolved owner decision. Include recommendation, viable alternatives, consequences and evidence.", []string{"project_id", "question", "recommendation", "why"}, []string{"options", "evidence"}),
		tool("remember_preference", "Remember an owner preference. This cannot grant permissions or change budgets.", []string{"key", "value"}, nil),
		tool("report_status", "Record an evidence-backed update on a project for the owner.", []string{"project_id", "summary"}, []string{"evidence"}),
	}
}
func tool(name, description string, strings, arrays []string) Tool {
	props := map[string]any{}
	required := []string{}
	for _, k := range strings {
		props[k] = map[string]any{"type": "string"}
		required = append(required, k)
	}
	for _, k := range arrays {
		props[k] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		required = append(required, k)
	}
	if name == "create_project" {
		props["directories"] = map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}, "maxItems": 16}
		required = append(required, "directories")
	}
	return Tool{Type: "function", Function: Function{Name: name, Description: description, Strict: true, Parameters: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}}
}
func knownTool(name string) bool {
	for _, t := range Tools() {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}
