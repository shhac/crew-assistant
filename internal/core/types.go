// Package core owns durable coordination records and deterministic authority checks.
package core

import "time"

type Assistant struct {
	Name        string `json:"name"`
	Personality string `json:"personality"`
}
type Project struct {
	Directories        []string  `json:"directories"`
	ScratchDirectory   string    `json:"scratch_directory"`
	ContractDefined    bool      `json:"contract_defined"`
	SourceDescription  string    `json:"source_description,omitempty"`
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	AcceptanceCriteria string    `json:"acceptance_criteria"`
	Status             string    `json:"status"`
	SourceID           string    `json:"source_id,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type Decision struct {
	Disposition      string     `json:"disposition,omitempty"`
	ResolutionReason string     `json:"resolution_reason,omitempty"`
	ID               string     `json:"id"`
	ProjectID        string     `json:"project_id,omitempty"`
	Title            string     `json:"title"`
	Context          string     `json:"context"`
	Recommendation   string     `json:"recommendation"`
	Choices          []string   `json:"choices"`
	Status           string     `json:"status"`
	Answer           string     `json:"answer,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
}
type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
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
	ChatCheckpoint    ChatCheckpoint     `json:"-"`
	ChatTurns         []ChatTurn         `json:"-"`
	ChatHold          *ChatHold          `json:"-"`
	ChatQueueRevision int                `json:"-"`
	PendingOperations []PendingOperation `json:"pending_operations"`
	Events            map[string]bool    `json:"-"`
	Assistant         Assistant          `json:"assistant"`
	Projects          []Project          `json:"projects"`
	Decisions         []Decision         `json:"decisions"`
	Messages          []Message          `json:"messages"`
	Memories          []Memory           `json:"memories"`
	Activity          []Activity         `json:"activity"`
	Integrations      []Integration      `json:"integrations"`
	Paused            bool               `json:"paused"`
	ModelCalls        map[string]int     `json:"-"`
}
type ProjectInput struct {
	Directories        []string `json:"directories"`
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	SourceID           string   `json:"source_id,omitempty"`
}

// DelegateInput requests a daemon-owned assignment. ParentID selects the
// responsible coordinator and constrains delegated capabilities.
type DecisionInput struct {
	ProjectID      string   `json:"project_id,omitempty"`
	Title          string   `json:"title"`
	Context        string   `json:"context"`
	Recommendation string   `json:"recommendation"`
	Choices        []string `json:"choices"`
}
