package roles

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// Exercise a real old instruction hash and the explicit fresh retry without
// inference or weakening the production command boundary.
func TestAPIInstructionMigrationOpensFreshWithToolReport(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: work, RuntimeHome: home,
		Instructions: "Read files outside the workspace only through run_command. You have no web search.",
		Tools:        []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}},
		Handler:      session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) { return session.ToolResult{}, nil }),
		Provider:     harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	opts := options(spec)
	opts.Workbench.Commands = nil
	old, _, err := open(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.Resume, _ = json.Marshal(old.Ref())
	if _, err := old.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	spec.Instructions = "Read and search files through run_command. read_file, search_files and edit_file are unavailable."
	spec.FreshPrompt = "Fresh task context"
	calls, openings := 0, 0
	var tools []session.WorkbenchTool
	spec.ToolReport = func(report []session.WorkbenchTool) { tools = report }
	spec.Opening = func(opened session.Opened, _ session.Ref) error {
		openings++
		if opened.Resumed || opened.Fresh != session.FreshIncompatible || len(tools) != 2 {
			t.Fatalf("migration opening: %+v tools=%v", opened, tools)
		}
		return nil
	}
	fixture := &fakeSession{ref: &session.Ref{}, confirmed: true}
	n := Native{open: func(ctx context.Context, o session.Options, raw json.RawMessage) (conversation, session.Opened, error) {
		calls++
		o.Workbench.Commands = nil
		if len(raw) > 0 {
			var ref session.Ref
			if err := json.Unmarshal(raw, &ref); err != nil {
				t.Fatal(err)
			}
			if _, err := session.Resume(ctx, o, ref); !errors.Is(err, session.ErrIncompatibleResume) {
				t.Fatalf("old instructions resumed: %v", err)
			}
			return nil, session.Opened{}, session.ErrIncompatibleResume
		}
		fresh, opened, err := open(ctx, o, nil)
		if err != nil {
			return nil, opened, err
		}
		*fixture.ref = fresh.Ref()
		report := fresh.Capabilities().WorkbenchTools
		if _, err := fresh.Release(ctx); err != nil {
			t.Fatal(err)
		}
		return toolReportingSession{fakeSession: fixture, tools: report}, opened, nil
	}}
	out, err := n.Run(context.Background(), spec)
	if err != nil || calls != 2 || openings != 1 || out.Opening == nil || out.Opening.Fresh != session.FreshIncompatible || !slices.Contains(fixture.calls, "turn: "+spec.FreshPrompt) {
		t.Fatalf("%+v %v calls=%d opening=%d prompt=%v", out, err, calls, openings, fixture.calls)
	}
}
