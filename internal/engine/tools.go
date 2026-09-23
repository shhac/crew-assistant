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
