package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
)

func TestContextSummaryDiagnosticsPreserveArchiveAndUsage(t *testing.T) {
	secret := "synthetic-private-summary"
	tests := []struct {
		name         string
		reply        Message
		code, reason string
	}{
		{"role", Message{Role: secret, Content: secret}, "invalid_context_summary_role", "invalid role"},
		{"tool", contextCall(secret), "context_summary_tool_calls", "requested tools"},
		{"empty", Message{Role: "assistant", Content: " \n "}, "empty_context_summary", "was empty"},
		{"oversized", Message{Role: "assistant", Content: strings.Repeat(secret, 100)}, "context_summary_too_large", "byte limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := contextFixture()
			calls := 0
			cp, usage, err := CompactContext(context.Background(), input, ContextOptions{MaxBytes: 40000, MaxSummaryBytes: 1024}, func(context.Context, []Message) (Message, Usage, error) {
				calls++
				return tt.reply, Usage{TotalTokens: 7, Known: true}, nil
			})
			var failure *completion.RequestError
			var safe interface{ SafeDiagnostic() string }
			if !errors.As(err, &failure) || failure.Cause != harness.CauseUnknown || failure.Phase != completion.PhaseResponse || failure.Code != tt.code || failure.Retryable() {
				t.Fatalf("missing nonretryable diagnostic: %v", err)
			}
			if !errors.As(err, &safe) || !strings.Contains(safe.SafeDiagnostic(), tt.reason) || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe or unhelpful diagnostic: %v", err)
			}
			wantCalls := 1
			if tt.name == "oversized" {
				wantCalls = 2
			}
			if calls != wantCalls || usage.TotalTokens != 7*wantCalls || !usage.Known || cp.Compacted || !reflect.DeepEqual(cp.Messages, input) {
				t.Fatal("failure changed archive, accounting, or call count")
			}
		})
	}
}

func TestContextPressureAndInvalidLimitsHaveLocalDiagnostics(t *testing.T) {
	input := []Message{{Role: "user", Content: strings.Repeat("immutable", 2000)}}
	for _, tt := range []struct {
		name     string
		opts     ContextOptions
		code     string
		pressure bool
	}{
		{"pressure", ContextOptions{MaxBytes: 10000}, "working_context_budget", true},
		{"limits", ContextOptions{MaxBytes: 100}, "invalid_context_limits", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cp, _, err := CompactContext(context.Background(), input, tt.opts, nil)
			var failure *completion.RequestError
			if !errors.As(err, &failure) || failure.Code != tt.code || failure.Phase != completion.PhasePreflight || failure.Retryable() {
				t.Fatalf("missing local diagnostic: %v", err)
			}
			if errors.Is(err, ErrContextPressure) != tt.pressure || !reflect.DeepEqual(cp.Messages, input) {
				t.Fatal("lost context identity or original messages")
			}
		})
	}
}

func TestCompleteInvalidConfigHasSafePreflightDiagnostic(t *testing.T) {
	codex := harness.Provider{Engine: harness.Codex}
	for _, cfg := range []Config{{Provider: harness.Provider{Engine: "private-model"}}, {Provider: codex, Model: "fixture", MaxOutputTokens: -1}, {Provider: codex}} {
		_, _, err := Complete(context.Background(), cfg, nil, nil)
		var failure *completion.RequestError
		if !errors.As(err, &failure) || failure.Code != "invalid_model_configuration" || failure.Phase != completion.PhasePreflight || failure.Retryable() {
			t.Fatalf("missing config diagnostic: %v", err)
		}
		if strings.Contains(err.Error(), "private-model") {
			t.Fatal("configuration value leaked")
		}
		if cfg.Provider.Engine == harness.Codex && cfg.Model == "" && !errors.Is(err, ErrNotConfigured) {
			t.Fatal("lost configuration sentinel")
		}
	}
}
