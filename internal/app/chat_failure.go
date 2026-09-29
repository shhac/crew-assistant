package app

import (
	"context"
	"errors"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
)

// Preserve useful next steps without copying raw provider, tool or storage errors
// into durable conversation metadata.
func chatFailureReason(err error) string {
	facts, classified := harness.ErrorFacts(err)
	if classified && facts.Code == harness.CodeKeychainUnavailable {
		return "Your login keychain is locked, so the model's CLI wasn't started. Unlock it, then send again. Recorded actions were preserved; no automatic replay was attempted."
	}
	if classified {
		switch facts.Cause {
		case harness.CauseOverloaded, harness.CauseUnavailable, harness.CauseRateLimited:
			return "The model provider remains unavailable or rate-limited after bounded recovery. Recorded actions were preserved; review them before asking to continue."
		case harness.CauseAuthentication, harness.CausePermissionDenied:
			return "The selected model login needs attention. Check its account in Settings; recorded actions were preserved."
		case harness.CauseContextLimit:
			return "The remaining context cannot fit safely. Original dialogue and recorded work were preserved; narrow the request before continuing."
		case harness.CauseModelUnavailable:
			return "The selected model isn't available to its login or endpoint. Choose another in Settings; recorded actions were preserved."
		}
	}
	detail := strings.ToLower(err.Error())
	reason := "The assistant could not finish this message."
	switch {
	case errors.Is(err, ErrNoAssistant):
		reason = "Choose your assistant in Settings before sending another message."
	case errors.Is(err, engine.ErrNotConfigured):
		reason = "Choose an assistant model in Settings before sending another message."
	case errors.Is(err, core.ErrModelCallAllowance):
		reason = "The daily model-call allowance is exhausted. Wait for it to reset or adjust the limit in Settings."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(detail, "timed out"):
		reason = "The reply timed out. Check the recorded actions before asking the assistant to continue."
	case errors.Is(err, engine.ErrContextPressure):
		reason = "The remaining instructions or unresolved work exceed the safe working-context budget. Original dialogue and recorded work were preserved; narrow the request before continuing."
	case strings.Contains(detail, "context limit"):
		reason = "The model context limit was reached. Original dialogue and recorded work were preserved."
	case errors.Is(err, engine.ErrTurnLimit):
		reason = "The assistant reached its per-turn action limit. Review its progress before asking it to continue."
	case classified && facts.Engine.Transport() == harness.CLITransport && (facts.Family == harness.FailurePreflight || facts.Family == harness.FailureProcess || facts.Family == harness.FailureCapability):
		reason = "Check the selected CLI installation, login, and model in Settings; the assistant could not use that profile."
	}
	return reason + " Recorded actions were preserved; no automatic replay was attempted."
}
