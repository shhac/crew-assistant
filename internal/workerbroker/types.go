// Package workerbroker runs coding workers inside owner-supplied, offline Docker
// images. The PA process never receives these implementation tools.
package workerbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

type DependencyMount struct {
	Source string
	Target string
}

type Config struct {
	Diagnostics  *diagnostics.Logger `json:"-"`
	Dependencies []DependencyMount   `json:"-"`

	StateDir        string
	Workspace       string
	ProjectID       string
	Image           string
	DockerSocket    string
	Engine          string
	Effort          string
	CodexBin        string
	CodexHome       string
	ClaudeBin       string
	ClaudeHome      string
	ModelEndpoint   string
	Model           string
	APIKeyEnv       string
	TokenEnv        string
	AuthToken       string `json:"-"` // In-process managed brokers never export credentials.
	MaxOutputTokens int
	MaxConcurrent   int
	// Admit runs before every billable inference, including context summaries
	// and recovery attempts. Returning a *worker.HoldError preserves the run's
	// work and waits; any other error stops the attempt. A nil callback permits
	// every request, which is what a broker with no configured policy does.
	Admit func(context.Context) error `json:"-"`
	// BridgeCommand overrides the command re-executed as the harness's tool
	// server. Production resolves this process's own binary; tests supply a
	// stand-in. It is never discovered from PATH.
	BridgeCommand session.Bridge `json:"-"`
	// TokenBudget is read immediately before each request so a live policy
	// change applies without restarting the broker. Zero disables the budget.
	TokenBudget func() int64 `json:"-"`
	HTTPClient  *http.Client
	// Command is injectable for tests. Production uses exec.CommandContext with a
	// clean environment; it never invokes a host shell.
	Command Commander
}
type Commander interface {
	Run(context.Context, []string, []byte) ([]byte, error)
}
type CommandFunc func(context.Context, []string, []byte) ([]byte, error)

func (f CommandFunc) Run(ctx context.Context, args []string, in []byte) ([]byte, error) {
	return f(ctx, args, in)
}

type storedRun struct {
	// Session is the assignment's one coding session. Everything about a worker's
	// continuity hangs off it: a resume reopens exactly this, and a new task gets
	// a new one rather than inheriting an old coding conversation.
	Session      *session.Ref `json:"session,omitempty"`
	SessionPhase string       `json:"session_phase,omitempty"`
	ToolDir      string       `json:"tool_dir,omitempty"`
	Briefed      bool         `json:"briefed,omitempty"`
	// InFlight is direction handed to a turn that has not confirmed accepting it.
	// It is replayed rather than dropped: an unacknowledged prompt is not a
	// delivered one, and losing the coordinator's words to a failed start is not
	// a recoverable state for the assignment.
	InFlight *inFlightInput `json:"in_flight,omitempty"`
	// Steering is direction handed to an already-running turn, pending the same
	// confirmation.
	Steering *inFlightInput `json:"steering,omitempty"`
	// Activity is the readable record of what happened. It is bounded and
	// sanitized, and it is not the evidence that decides acceptance.
	Activity        []activityEntry `json:"activity,omitempty"`
	ActivityDropped bool            `json:"activity_dropped,omitempty"`
	// UnsettledCommands counts commands that passed their limit without being
	// confirmed stopped. Each one means the workspace may still be changing, so
	// an acceptance report on top of them is held rather than published.
	UnsettledCommands int `json:"unsettled_commands,omitempty"`
	// Observed columns mirror the published ledger's evidence figures.
	ObservedInputTokens  int64 `json:"observed_input_tokens,omitempty"`
	ObservedOutputTokens int64 `json:"observed_output_tokens,omitempty"`
	// Transcript is the archive of an assignment started under the previous
	// contract. It is preserved for inspection and converted once, on an
	// explicit owner resume, into a handover brief. It is never a coding session.
	Transcript []modelMessage `json:"transcript"`
	// LegacyNotified records that the owner has been told this assignment
	// predates the coding-session contract and what carries over. The handover
	// happens on the resume that follows, so an owner who would rather accept
	// what it already produced is never overtaken by it.
	LegacyNotified bool                `json:"legacy_notified,omitempty"`
	Migrated       bool                `json:"migrated,omitempty"`
	PendingMessage *worker.PeerMessage `json:"pending_message,omitempty"`
	PendingStatus  string              `json:"pending_status,omitempty"`
	PendingSummary string              `json:"pending_summary,omitempty"`
	Run            worker.Run          `json:"run"`
	Request        worker.StartRequest `json:"request"`
	Container      string              `json:"container"`
	WorkDir        string              `json:"work_dir"`
	Baseline       map[string][]byte   `json:"baseline"`
	Messages       []string            `json:"messages"`
	Commands       []commandRecord     `json:"commands"`
	// ModelCalls is diagnostic history. It was once a work allowance; it is not
	// one now, and a resume never resets it.
	ModelCalls int `json:"model_calls"`
	// PendingUsage is written after admission and before the request leaves, so
	// a crash cannot turn a possibly-billed call into free work. It is settled
	// by request ID, which makes a late or duplicate settlement a no-op.
	PendingUsage *pendingUsage `json:"pending_usage,omitempty"`
	// UsageLedger marks that consumption has been accounted since this run
	// started recording it. A run persisted before the ledger existed converts
	// its historical calls into unknown consumption exactly once.
	UsageLedger       bool  `json:"usage_ledger,omitempty"`
	UsageInputTokens  int64 `json:"usage_input_tokens,omitempty"`
	UsageOutputTokens int64 `json:"usage_output_tokens,omitempty"`
	UsageUnknownCalls int   `json:"usage_unknown_calls,omitempty"`
}

// inFlightInput is something the daemon has taken off its queue and is trying
// to say. It exists so "taken off the queue" and "delivered" are different
// states, which is what makes an interrupted delivery recoverable.
type inFlightInput struct {
	Text     string    `json:"text"`
	Briefing bool      `json:"briefing,omitempty"`
	Messages int       `json:"messages,omitempty"`
	At       time.Time `json:"at"`
	// Delivery is how far this got. The distinction matters because the obvious
	// reading of a missing acknowledgement — "it was never received" — is false
	// exactly when it is most expensive: a harness that took the prompt, ran
	// tools and then lost its process leaves no acknowledgement either.
	Delivery string `json:"delivery,omitempty"`
}

// Delivery states for an input the daemon is trying to hand to a worker.
const (
	// deliveryUnsent: nothing has been handed over. Saying it is safe.
	deliveryUnsent = "unsent"
	// deliverySending: handed over, outcome unknown. A process that dies here
	// leaves this behind, which is what makes it uncertain rather than unsent.
	deliverySending = "sending"
	// deliveryUnknown: the attempt ended without establishing either. Repeating
	// it might duplicate work the worker already did, so a person decides.
	deliveryUnknown = "uncertain"
)

type pendingUsage struct {
	RequestID string    `json:"request_id"`
	Stage     string    `json:"stage"`
	StartedAt time.Time `json:"started_at"`
}
type commandRecord struct {
	Command string `json:"command"`
	Success bool   `json:"success"`
	Output  string `json:"output"`
}
type receipt struct {
	Digest string `json:"digest"`
	RunID  string `json:"run_id"`
}
type diskState struct {
	Runs     map[string]*storedRun `json:"runs"`
	Receipts map[string]receipt    `json:"receipts"`
}
type modelMessage = engine.Message
type toolCall = engine.ToolCall

var ErrConflict = errors.New("worker operation conflicts with its persisted state")
var errInterrupted = errors.New("worker interrupted before acceptance; inspect evidence before resuming")

func strict(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid request JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

// Bytes reader is kept here only to share strict decoding across model tools and HTTP.
func now() time.Time { return time.Now().UTC() }
