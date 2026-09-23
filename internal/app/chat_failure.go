package app

import (
	"context"
	"errors"
	"strings"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/completion"
)

// Preserve useful next steps without copying raw provider, tool or storage errors
// into durable conversation metadata.
func chatFailureReason(err error) string {
	var failure *completion.RequestError
	if errors.As(err, &failure) {
		switch failure.Kind {
		case completion.ErrorOverloaded, completion.ErrorUnavailable, completion.ErrorRateLimited:
			return "The model provider remains unavailable or rate-limited after bounded recovery. Recorded actions were preserved; review them before asking to continue."
		case completion.ErrorAuthentication:
			return "The selected model login needs attention. Check its account in Settings; recorded actions were preserved."
		case completion.ErrorContextLimit:
			return "The remaining context cannot fit safely. Original dialogue and recorded work were preserved; narrow the request before continuing."
		}
	}
	detail := strings.ToLower(err.Error())
	reason := "The assistant could not finish this message."
	switch {
	case errors.Is(err, engine.ErrNotConfigured):
		reason = "Choose an assistant model in Settings before sending another message."
	case strings.Contains(detail, "daily model call allowance"):
		reason = "The daily model-call allowance is exhausted. Wait for it to reset or adjust the limit in Settings."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(detail, "timed out"):
		reason = "The reply timed out. Check the recorded actions before asking the assistant to continue."
	case errors.Is(err, engine.ErrContextPressure):
		reason = "The remaining instructions or unresolved work exceed the safe working-context budget. Original dialogue and recorded work were preserved; narrow the request before continuing."
	case strings.Contains(detail, "context limit"):
		reason = "The model context limit was reached. Original dialogue and recorded work were preserved."
	case errors.Is(err, engine.ErrTurnLimit):
		reason = "The assistant reached its per-turn action limit. Review its progress before asking it to continue."
	case strings.Contains(detail, "codex") || strings.Contains(detail, "claude"):
		reason = "Check the selected CLI installation, login, and model in Settings; the assistant could not use that profile."
	}
	return reason + " Recorded actions were preserved; no automatic replay was attempted."
}
