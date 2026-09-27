package engine

import (
	"strings"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
)

// These diagnostics receive only application-owned constants, never model data.
type completionDiagnostic struct {
	message string
	failure *completion.RequestError
}

func (e *completionDiagnostic) Error() string          { return e.message }
func (e *completionDiagnostic) SafeDiagnostic() string { return e.message }
func (e *completionDiagnostic) Unwrap() error          { return e.failure }
func localCompletionDiagnostic(message string, cause harness.Cause, phase completion.ErrorPhase, code string) error {
	return &completionDiagnostic{message: message, failure: &completion.RequestError{Cause: cause, Phase: phase, Code: code}}
}

func validateContextSummary(reply Message, maxBytes int) error {
	var reason, code string
	switch {
	case reply.Role != "assistant":
		reason, code = "context summary has an invalid role", "invalid_context_summary_role"
	case len(reply.ToolCalls) > 0:
		reason, code = "context summary requested tools", "context_summary_tool_calls"
	case strings.TrimSpace(reply.Content) == "":
		reason, code = "context summary was empty", "empty_context_summary"
	case len(reply.Content) > maxBytes:
		reason, code = "context summary exceeded its byte limit", "context_summary_too_large"
	default:
		return nil
	}
	return localCompletionDiagnostic(reason+"; original context preserved", harness.CauseUnknown, completion.PhaseResponse, code)
}
