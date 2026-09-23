package engine

import "github.com/shhac/lib-agent-harness/completion"

// Tool argument types are shared with the application's strict, policy-checking bridge.
type CreateProjectArgs struct {
	Directories        []string `json:"directories"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}
type UpdateProjectArgs struct {
	Directories        []string `json:"directories"`
	ProjectID          string   `json:"project_id"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
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

		tool("read_state", "Read current projects, work, decisions, preferences, available profiles and authority.", nil, nil),
		tool("create_project", "Track a project in local assistant state with a title and optional existing absolute directory paths. No Linear issue, external tracker, or connection is required. Objective may be empty and acceptance_criteria may be empty until commissioning. This creates coordination metadata only; it never opens or edits project files.", []string{"title", "objective"}, []string{"acceptance_criteria"}),
		tool("update_project", "Refine an uncommissioned project brief into concrete acceptance criteria before delegating. Optionally link existing absolute directories; null preserves current links. Cannot change the acceptance contract after workers are commissioned.", []string{"project_id", "objective"}, []string{"acceptance_criteria"}),
		tool("ask_decision", "Prepare an unresolved owner decision. Include recommendation, viable alternatives, consequences and evidence.", []string{"project_id", "question", "recommendation", "why"}, []string{"options", "evidence"}),
		tool("remember_preference", "Remember an owner preference. This cannot grant permissions or change budgets.", []string{"key", "value"}, nil),
		tool("report_status", "Record an evidence-backed coordination update; this does not mark project work accepted or completed.", []string{"project_id", "summary"}, []string{"evidence"}),
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
	if name == "create_project" || name == "update_project" {
		props["directories"] = map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}, "maxItems": 16}
		required = append(required, "directories")
	}
	if name == "control_agent" {
		props["action"] = map[string]any{"type": "string", "enum": []string{"pause", "resume", "stop"}}
	}
	if name == "delegate" {
		props["role"] = map[string]any{"type": "string", "enum": []string{"worker", "manager"}}
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
