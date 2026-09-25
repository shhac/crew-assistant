// Package core owns durable coordination records and deterministic authority checks.
package core

import (
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

type Assistant struct {
	Name        string        `json:"name"`
	Personality string        `json:"personality"`
	Theme       string        `json:"theme"`
	Avatar      config.Avatar `json:"avatar"`
	// AvatarSVG is the avatar drawn, for the dashboard's tab icon and chat.
	AvatarSVG string `json:"avatar_svg,omitempty"`
	Drawing   bool   `json:"drawing,omitempty"`
	DrawError string `json:"draw_error,omitempty"`
}

// Project is an ongoing area of the owner's work: what it is for (its brief),
// how its work gets done (its playbook) and where it lives.
type Project struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Status            string    `json:"status"`
	Brief             Brief     `json:"brief"`
	Playbook          *Playbook `json:"playbook,omitempty"`
	Directories       []string  `json:"directories"`
	ScratchDirectory  string    `json:"scratch_directory"`
	SourceID          string    `json:"source_id,omitempty"`
	SourceDescription string    `json:"source_description,omitempty"`
	// Landed is the project's most recently delivered code change. Other work
	// in the project catches up with it before it is delivered.
	Landed *Landing `json:"landed,omitempty"`
	// OrderedBy says who last set the to-do order: the owner, the assistant
	// or the team's PM. The owner's or the assistant's order stands over the
	// PM's until new work arrives.
	OrderedBy string    `json:"ordered_by,omitempty"`
	OrderedAt time.Time `json:"ordered_at,omitempty"`
	// PMDue asks the team's PM to look at the to-do list again, after
	// something that changes it: work queued, planned or finished.
	PMDue bool `json:"pm_due,omitempty"`
	// PMDirection is what the owner told the PM, for its next look.
	PMDirection string    `json:"pm_direction,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Who set a project's to-do order.
const (
	OrderedByOwner     = "owner"
	OrderedByAssistant = "assistant"
	OrderedByPM        = "pm"
)

// Landing is one delivered code change: the commit and the branch it went to.
type Landing struct {
	TaskID    string    `json:"task_id"`
	Objective string    `json:"objective"`
	Commit    string    `json:"commit"`
	Branch    string    `json:"branch"`
	At        time.Time `json:"at"`
}

// How a decision was closed. Only DispositionChoice can approve, stop or
// retry a task; DispositionCustom is the owner's own words.
const (
	DispositionChoice    = "choice"
	DispositionCustom    = "custom"
	DispositionDismissed = "dismissed"
)

// Decision kinds. A choice is an ordinary decision; the rest hold a task.
const (
	DecisionChoice   = "choice"
	DecisionDelivery = "delivery"
	// DecisionUpdate holds an update to an open pull request that changes
	// what runs or instructs on the owner's side.
	DecisionUpdate     = "update"
	DecisionQuestion   = "question"
	DecisionEscalation = "escalation"
	DecisionFailure    = "failure"
)

// Decision statuses.
const (
	DecisionOpen      = "open"
	DecisionResolved  = "resolved"
	DecisionDismissed = "dismissed"
)

type Decision struct {
	Disposition      string `json:"disposition,omitempty"`
	ResolutionReason string `json:"resolution_reason,omitempty"`
	ID               string `json:"id"`
	ProjectID        string `json:"project_id,omitempty"`
	// TaskID and Kind tie a decision to the task it holds.
	TaskID         string     `json:"task_id,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	Title          string     `json:"title"`
	Context        string     `json:"context"`
	Recommendation string     `json:"recommendation"`
	Choices        []string   `json:"choices"`
	Status         string     `json:"status"`
	Answer         string     `json:"answer,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
}

// Approves reports a decision whose approval lets the task's change go out.
func (d Decision) Approves() bool {
	return d.Kind == DecisionDelivery || d.Kind == DecisionUpdate
}

type Message struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	// Origin marks a user-role message the daemon wrote, such as a wake-up,
	// so neither the owner nor the model mistakes it for the owner's words.
	Origin    string    `json:"origin,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Memory separates a durable preference from a time-sensitive observation so a
// stale fact is not read as a standing instruction. Kind and Source are empty
// for anything recorded before they existed; that is reported as uncategorized
// rather than guessed at. Correcting a memory supersedes it and keeps the
// original, so the record of what was believed is never silently rewritten.
type Memory struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Content      string    `json:"content"`
	Kind         string    `json:"kind,omitempty"`
	Source       string    `json:"source,omitempty"`
	Supersedes   string    `json:"supersedes,omitempty"`
	SupersededAt time.Time `json:"superseded_at,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}
type Activity struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id,omitempty"`
	Kind      string    `json:"kind"`
	Summary   string    `json:"summary"`
	CreatedAt time.Time `json:"created_at"`
}

type Integration struct {
	ID string `json:"id"`
	// ProjectID links a connection to the project it serves, where one does.
	// The dashboard shows a capacity hold beside the work it holds up, and
	// that relationship is the daemon's to state rather than the browser's to
	// reassemble from an identifier convention.
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
}
type PendingOperation struct {
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	ProjectID string `json:"project_id,omitempty"`
}

type Snapshot struct {
	ChatCheckpoint ChatCheckpoint `json:"-"`
	// ChatSession is the current conversation's model session.
	ChatSession *ChatSession `json:"-"`
	// ConversationID names the conversation Messages holds; Conversations
	// are the ones archived by /new and /clear.
	ConversationID    string             `json:"-"`
	Conversations     []Conversation     `json:"-"`
	ChatTurns         []ChatTurn         `json:"-"`
	ChatHold          *ChatHold          `json:"-"`
	ChatQueueRevision int                `json:"-"`
	PendingOperations []PendingOperation `json:"pending_operations"`
	Events            map[string]bool    `json:"-"`
	Assistant         Assistant          `json:"assistant"`
	Projects          []Project          `json:"projects"`
	Tasks             []Task             `json:"tasks"`
	Decisions         []Decision         `json:"decisions"`
	Messages          []Message          `json:"messages"`
	Memories          []Memory           `json:"memories"`
	Activity          []Activity         `json:"activity"`
	Integrations      []Integration      `json:"integrations"`
	Members           []Member           `json:"members"`
	Paused            bool               `json:"paused"`
	// Stopping says the daemon is finishing the work in progress before it
	// stops; nothing new starts. It is the running daemon's, never stored.
	Stopping   bool           `json:"stopping,omitempty"`
	Wakes      []Wake         `json:"wakes,omitempty"`
	ModelCalls map[string]int `json:"-"`
	// ModelWindows are the context windows, in tokens, the providers have
	// stated for each engine's models, keyed by ModelKey.
	ModelWindows map[string]int `json:"-"`
}
type ProjectInput struct {
	Directories       []string   `json:"directories"`
	Title             string     `json:"title"`
	Brief             BriefInput `json:"brief"`
	Template          string     `json:"template"`
	SourceID          string     `json:"source_id,omitempty"`
	SourceDescription string     `json:"source_description,omitempty"`
}

type DecisionInput struct {
	ProjectID      string   `json:"project_id,omitempty"`
	Title          string   `json:"title"`
	Context        string   `json:"context"`
	Recommendation string   `json:"recommendation"`
	Choices        []string `json:"choices"`
}
