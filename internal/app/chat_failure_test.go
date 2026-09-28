package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
	"github.com/shhac/lib-agent-harness/session"
)

func TestChatFailureGivesSafeNextStep(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{engine.ErrNotConfigured, "Choose an assistant model"},
		{errors.New("daily model call allowance exhausted"), "daily model-call allowance"},
		{fmt.Errorf("PRIVATE-DIAGNOSTIC: %w", &session.ProcessError{Engine: harness.Codex, Code: session.ProcessExited}), "Check the selected CLI"},
		{fmt.Errorf("PRIVATE-DIAGNOSTIC: %w", &completion.RequestError{Engine: harness.OpenAICompatible, Cause: harness.CauseModelUnavailable, Phase: completion.PhaseResponse}), "isn't available"},
		{&completion.RequestError{Engine: harness.Claude, Cause: harness.CauseRateLimited, Phase: completion.PhaseResponse}, "rate-limited"},
		{errors.New("private backend diagnostic PRIVATE-DIAGNOSTIC"), "could not finish"},
		{fmt.Errorf("PRIVATE-DIAGNOSTIC: %w", &completion.RequestError{Engine: harness.Claude, Code: harness.CodeKeychainUnavailable, Phase: completion.PhasePreflight}), "keychain is locked"},
	} {
		got := chatFailureReason(tc.err)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE-DIAGNOSTIC") || !strings.Contains(got, "preserved") {
			t.Fatal(got)
		}
	}
}
