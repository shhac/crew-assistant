package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func TestTheChatsSessionHasOnlyTheAssistantsToolsInFoldersOfItsOwn(t *testing.T) {
	state := t.TempDir()
	spec := chatSpec{
		Config:       engine.Config{Provider: harness.Provider{Engine: harness.Codex, CLI: harness.CLI{Binary: "/usr/local/bin/codex"}}, Model: "gpt"},
		Instructions: "Be brief.",
		StateDir:     state,
		Tool: func(context.Context, string, json.RawMessage) session.ToolResult {
			return session.ToolResult{Content: engine.ToolDeclined, IsError: true}
		},
		Context: func(context.Context, session.ContextReason) (string, error) { return "", nil },
	}
	o, err := chatSessionOptions(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{o.WorkDir, o.RuntimeHome, o.Restriction.Tools.Dir} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 || !strings.HasPrefix(dir, filepath.Join(state, "chat")+string(filepath.Separator)) {
			t.Errorf("%s: %v %v", dir, info, err)
		}
	}
	host := o.Restriction.Tools
	if len(host.Tools) != len(engine.Tools()) || host.Bridge.Args[0] != roles.ToolBridge || !filepath.IsAbs(host.Bridge.Path) {
		t.Fatalf("tools %d, bridge %+v", len(host.Tools), host.Bridge)
	}
	if o.Instructions.Mode != session.Append || o.Instructions.Text != "Be brief." || o.Model != "gpt" || o.Provider.CLI.Binary != "/usr/local/bin/codex" {
		t.Fatalf("options %+v", o)
	}
	result, err := host.Handler.CallTool(context.Background(), session.ToolCall{Name: "read_state", Arguments: json.RawMessage(`{}`)})
	if err != nil || !result.IsError {
		t.Fatalf("a declined call should reach the model as a failure: %+v %v", result, err)
	}
}

// The assistant never writes files or runs a shell, so a Grok chat session
// refuses whatever Grok asks to do; other engines keep the policy their
// stored conversations were started with.
func TestAGrokChatSessionRefusesWhatItIsAskedToDo(t *testing.T) {
	for engineName, want := range map[harness.Engine]session.Policy{
		harness.Grok:   {GrokPermission: session.GrokDenyWhenAsked, GrokTelemetry: session.GrokTelemetryReduced},
		harness.Codex:  {},
		harness.Claude: {},
	} {
		spec := chatSpec{Config: engine.Config{Provider: harness.Provider{Engine: engineName}, Model: "m"}, StateDir: t.TempDir()}
		o, err := chatSessionOptions(spec)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(o.Policy, want) {
			t.Errorf("%s policy %+v, want %+v", engineName, o.Policy, want)
		}
	}
}
