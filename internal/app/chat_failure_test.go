package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/testutil"
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
		{core.ErrModelCallAllowance, "daily model-call allowance"},
		{allowanceRefusedByEngine(t), "daily model-call allowance"},
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

// allowanceRefusedByEngine is the spent allowance as a chat sees it: refused
// before the model is called, and wrapped by the engine on the way out.
func allowanceRefusedByEngine(t *testing.T) error {
	t.Helper()
	server := testutil.NewModelServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the model was called with the allowance spent")
	}))
	defer server.Close()
	provider := config.Harness{Engine: string(harness.OpenAICompatible), BaseURL: server.URL}.Provider()
	e, err := engine.New(engine.Config{Provider: provider, Model: "fixture", BeforeRequest: func(context.Context) error { return core.ErrModelCallAllowance }}, engine.ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Chat(context.Background(), engine.Request{Message: "Check"})
	if err == nil || err == core.ErrModelCallAllowance {
		t.Fatalf("the engine should refuse with the allowance wrapped: %v", err)
	}
	return err
}
