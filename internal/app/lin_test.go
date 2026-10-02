package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	harness "github.com/shhac/lib-agent-harness"
)

func TestAssistantLinExposureGuidanceAndSessionKey(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	cfg := a.Config()
	ec := a.assistantConfig(ctx, cfg)
	if ec.Lin || ec.LinGuidance != "" || engine.CheckToolCall("lin", json.RawMessage(`{}`), ec.Lin) == nil {
		t.Fatal("unconnected assistant offered lin")
	}
	key := chatKey("conversation", ec, ec.LinGuidance, config.Browser{})
	cfg.Connections = []config.Connection{{ID: "linear", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	ec = a.assistantConfig(ctx, cfg)
	if !ec.Lin || !strings.Contains(ec.LinGuidance, "Use the lin tool") || engine.CheckToolCall("lin", json.RawMessage(`{}`), ec.Lin) != nil || key == chatKey("conversation", ec, ec.LinGuidance, config.Browser{}) {
		t.Fatal("connected assistant missing capability or changed key")
	}
	for _, enabled := range []bool{false, true} {
		spec := chatSpec{Config: engine.Config{Lin: enabled, Provider: harness.Provider{Engine: harness.Codex}}, StateDir: t.TempDir()}
		o, err := chatSessionOptions(spec)
		if err != nil {
			t.Fatal(err)
		}
		host := chatTools(&o)
		found := false
		for _, d := range host.Tools {
			found = found || d.Name == "lin"
		}
		if found != enabled {
			t.Fatalf("session lin=%v: %v", enabled, host.Tools)
		}
	}
}
func TestAssistantLinPermissionFailureAndActivity(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "linear", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	count := 0
	a.connectionClient = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) { return []byte(`{"alias":"home"}`), nil }, RunClipped: func(_ context.Context, _ string, args []string) connections.LinResult {
		count++
		if !slices.Equal(args[:4], []string{"--color", "never", "--workspace", "home"}) {
			t.Fatal(args)
		}
		return connections.LinResult{Failed: true, Error: "sign in again", Output: "partial data"}
	}}
	invoke := func(id, profile string, args []string) connections.LinResult {
		raw, _ := json.Marshal(map[string]any{"connection_id": id, "profile": profile, "args": args, "reference": ""})
		out, err := a.Execute(ctx, "lin", raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(connections.LinResult)
	}
	read := []string{"issue", "get", "ENG-1"}
	out := invoke("linear", "home", read)
	if !out.Failed || out.Error != "sign in again" || out.Output != "partial data" || count != 1 {
		t.Fatal(out)
	}
	for _, scope := range [][2]string{{"other", "home"}, {"linear", "other"}} {
		if !invoke(scope[0], scope[1], read).Failed || count != 1 {
			t.Fatal("invalid account called lin")
		}
	}
	write := []string{"issue", "comment", "new", "ENG-1", "private body"}
	if !invoke("linear", "home", write).Failed || count != 1 {
		t.Fatal("read-only default changed Linear")
	}
	cfg.Connections[0].AllowWrites = true
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	invoke("linear", "home", write)
	if count != 2 {
		t.Fatal("opt-in did not run")
	}
	snap, _ := a.Core.Snapshot(ctx)
	var activity []string
	for _, e := range snap.Activity {
		if e.Kind == "linear.write" {
			activity = append(activity, e.Summary)
		}
	}
	if len(activity) != 2 || strings.Contains(strings.Join(activity, " "), "private body") || !strings.Contains(strings.Join(activity, " "), "Quill is changing Linear") {
		t.Fatalf("activity: %v", activity)
	}
	a.Demo = true
	if !invoke("linear", "home", read).Failed || count != 2 {
		t.Fatal("demo ran lin")
	}
	a.Demo = false
	cfg.Connections = nil
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if !invoke("linear", "home", read).Failed || count != 2 {
		t.Fatal("removed connection ran lin")
	}
}

func TestLinFailureReachesModelAsInformation(t *testing.T) {
	a := testApp(t)
	a.Demo = true
	out, _, err := engine.RunTool(context.Background(), a, nil, "call", "lin", json.RawMessage(`{"connection_id":"linear","profile":"home","reference":"","args":["issue","list"]}`))
	if err != nil || strings.Contains(out, engine.ToolDeclined) || !strings.Contains(out, `"failed":true`) || !strings.Contains(out, "demo mode") {
		t.Fatalf("model reply: %s %v", out, err)
	}
}
