package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
)

// The reply cap goes only to API endpoints, as it always has, and only when the
// harness can prove it applies; the others get none rather than failing.
func TestTheReplyCapGoesOnlyWhereTheHarnessCanApplyIt(t *testing.T) {
	for engine, want := range map[harness.Engine]int{
		harness.Claude:           0,
		harness.OpenAICompatible: 4096,
		harness.Codex:            0,
		harness.Grok:             0,
	} {
		if got := outputCap(engine, 4096); got != want {
			t.Errorf("%s: cap %d, want %d", engine, got, want)
		}
	}
}

func TestAnEngineWithoutAReplyCapIsNotRefusedForOne(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, engine := range []harness.Engine{harness.Codex, harness.Grok} {
		cfg := Config{Provider: harness.Provider{Engine: engine, CLI: harness.CLI{Binary: missing}}, Model: "m", WorkDirRoot: t.TempDir(), MaxOutputTokens: 4096}
		_, _, err := Complete(context.Background(), cfg, []Message{{Role: "user", Content: "hello"}}, nil)
		var failure *completion.RequestError
		if !errors.As(err, &failure) || failure.Code == "max_output_tokens_unsupported" {
			t.Errorf("%s: %v", engine, err)
		}
	}
}
