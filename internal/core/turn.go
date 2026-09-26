package core

import "time"

// Turn is a role at work on a task right now, with what its session has
// reported so far: the counts only go up while it is alive.
type Turn struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id,omitempty"`
	// Role is the kind of work, such as implementer; Seat is the seat's
	// name, and Member the team member in it, if any.
	Role   string `json:"role"`
	Seat   string `json:"seat"`
	Member string `json:"member,omitempty"`
	// StartedAt is when the turn was picked up; LastActivityAt when its
	// session last reported anything.
	StartedAt      time.Time `json:"started_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	ToolCalls      int       `json:"tool_calls"`
	// Edits are the tool calls that changed files.
	Edits int `json:"edits"`
	// Tool is the tool running now, if one is.
	Tool         string `json:"tool,omitempty"`
	OutputTokens int64  `json:"output_tokens"`
	// FilesChanged is how many files differ from where the round started,
	// looked at every few seconds, for a turn that writes; nil otherwise.
	FilesChanged *int `json:"files_changed,omitempty"`
}
