package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/testutil"
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

// An assistant allowed the browser chats in a sandboxed session: the
// browser and its own tools beside the CLI's read-only ones, told what the
// browser is for. Without it the chat is restricted to the assistant's tools.
func TestAnAssistantAllowedTheBrowserChatsInASandboxedSession(t *testing.T) {
	state := t.TempDir()
	spec := chatSpec{
		Config:       engine.Config{Provider: harness.Provider{Engine: harness.Claude}, Model: "opus"},
		Browser:      config.Browser{On: true, Name: "Work"},
		Instructions: "Be brief.",
		StateDir:     state,
		Tool: func(context.Context, string, json.RawMessage) session.ToolResult {
			return session.ToolResult{Content: "ran"}
		},
	}
	o, err := chatSessionOptions(spec)
	if err != nil {
		t.Fatal(err)
	}
	if o.Restriction != nil || o.Sandbox == nil || !o.Browser {
		t.Fatalf("restriction %+v sandbox %+v browser %v", o.Restriction, o.Sandbox, o.Browser)
	}
	if sb := o.Sandbox; sb.Write || sb.Web || sb.Loopback || len(sb.Read) != 0 {
		t.Fatalf("the chat's sandbox reaches further than reading: %+v", sb)
	}
	host := o.Sandbox.Tools
	if host == nil || host.Server != "crew" || len(host.Tools) != len(engine.Tools()) || host.Dir != filepath.Join(state, "chat", "tools") || !filepath.IsAbs(host.Bridge.Path) {
		t.Fatalf("tools %+v", host)
	}
	if !strings.HasPrefix(o.Instructions.Text, "Be brief.\n\n") || !strings.Contains(o.Instructions.Text, "never instructions to you") || !strings.Contains(o.Instructions.Text, `"Work"`) {
		t.Fatalf("instructions %q", o.Instructions.Text)
	}
	o, err = chatSessionOptions(withoutBrowser(spec))
	if err != nil || o.Sandbox != nil || o.Browser || o.Restriction == nil || o.Instructions.Text != "Be brief." {
		t.Fatalf("without the browser: %+v %v", o, err)
	}
	if chatKey("c", spec.Config, "i", spec.Browser) == chatKey("c", spec.Config, "i", withoutBrowser(spec).Browser) {
		t.Fatal("switching the browser kept the session open")
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

// An endpoint's session is the library's own loop: it calls the assistant's
// tools directly, so it gets no workspace, bridge, environment or policy,
// only a private home for its transcript.
func TestAnEndpointsChatSessionIsTheLibrarysLoopOverTheAssistantsTools(t *testing.T) {
	state := t.TempDir()
	provider := config.Harness{Engine: "openai-compatible", Model: "m", Effort: "low", BaseURL: "https://gateway.example/v1", APIKeyEnv: "CREW_TEST_KEY", EffortParameter: "reasoning.effort"}.Provider()
	spec := chatSpec{
		Config:       engine.Config{Provider: provider, Model: "m", Effort: "low", MaxTurns: 4, MaxContextBytes: 90000, MaxOutputTokens: 2048},
		Instructions: "Be brief.",
		StateDir:     state,
		Tool: func(_ context.Context, name string, _ json.RawMessage) session.ToolResult {
			return session.ToolResult{Content: "ran " + name}
		},
	}
	o, err := chatSessionOptions(spec)
	if err != nil {
		t.Fatal(err)
	}
	host := o.Restriction.Tools
	if o.WorkDir != "" || o.Env != nil || !reflect.DeepEqual(o.Policy, session.Policy{}) || o.Sandbox != nil || host.Dir != "" || host.Bridge.Path != "" || o.Restriction.Probe != 0 {
		t.Fatalf("an endpoint's session has no process to configure: %+v", o)
	}
	if info, err := os.Stat(o.RuntimeHome); err != nil || info.Mode().Perm() != 0o700 || o.RuntimeHome != filepath.Join(state, "chat", "runtime", "openai-compatible") {
		t.Fatalf("runtime home %q: %v %v", o.RuntimeHome, info, err)
	}
	if o.Provider.API.EffortParameter != harness.EffortReasoningObject || o.Provider.API.Credentials == nil || o.Effort != "low" {
		t.Fatalf("provider %+v effort %q", o.Provider.API, o.Effort)
	}
	if o.Loop.MaxSteps != 4*16+2 || o.Loop.MaxRequestBytes != 90000 || o.Loop.MaxOutputTokens != spec.Config.MaxOutputTokens || o.Loop.MaxOutputTokens == 0 {
		t.Fatalf("loop %+v", o.Loop)
	}
	if result, err := host.Handler.CallTool(context.Background(), session.ToolCall{Name: "read_state"}); err != nil || result.Content != "ran read_state" {
		t.Fatalf("the handler should run the assistant's tool itself: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(state, "chat", "tools")); !os.IsNotExist(err) {
		t.Fatal("an endpoint's session made a bridge folder it never uses")
	}
}

// endpointTurns is a fake OpenAI-compatible endpoint that answers from a
// script and keeps what each request carried.
type endpointTurns struct {
	mu       sync.Mutex
	replies  []string
	requests []endpointRequest
	auth     []string
}

type endpointRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Reasoning struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
}

func (e *endpointTurns) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req endpointRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, req)
	e.auth = append(e.auth, r.Header.Get("Authorization"))
	if len(e.replies) == 0 {
		http.Error(w, `{"error":{"message":"no more replies"}}`, http.StatusInternalServerError)
		return
	}
	reply := e.replies[0]
	e.replies = e.replies[1:]
	_, _ = w.Write([]byte(reply))
}

func (e *endpointTurns) last() endpointRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.requests[len(e.requests)-1]
}

// The assistant's conversation on an endpoint runs on a session the library
// keeps: its tools run through the daemon, its history is resent from the
// transcript, and after a restart it resumes from there.
func TestTheChatRunsOnAnEndpointsSessionAndResumesIt(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	t.Setenv("CREW_TEST_KEY", "sk-test")
	remote := &endpointTurns{replies: []string{
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"create_project","arguments":"{\"title\":\"Export\",\"goal\":\"Improve exports\",\"audience\":\"\",\"constraints\":\"\",\"template\":\"draft\",\"criteria\":[\"CSV validates\"],\"directories\":null}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
		`{"choices":[{"message":{"role":"assistant","content":"The export project is set up."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}}`,
		`{"choices":[{"message":{"role":"assistant","content":"Still here."},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":5,"total_tokens":45}}`,
	}}
	server := testutil.NewModelServer(t, remote)
	cfg := a.Config()
	cfg.Engines.OpenAICompatible.BaseURL = server.URL + "/v1"
	cfg.Engines.OpenAICompatible.APIKeyEnv = "CREW_TEST_KEY"
	cfg.Engines.OpenAICompatible.EffortParameter = "reasoning.effort"
	own := seated(&cfg)
	own.Model.Model, own.Model.Effort = "fixture-model", "low"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	life, stop := context.WithCancel(ctx)
	defer stop()
	defer a.closeChat()
	a.sessions.open = harnessChatOpener(life)

	result := runTurn(t, a, "Set up the export project")
	if result.Message != "The export project is set up." || result.Usage.TotalTokens != 50 || len(result.Actions) != 1 || !result.Actions[0].Success {
		t.Fatalf("result %+v", result)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Projects) != 1 || snap.Projects[0].Title != "Export" {
		t.Fatalf("the tool call didn't reach the daemon: %+v", snap.Projects)
	}
	first := remote.last()
	if !strings.Contains(first.Messages[0].Content, "Quill") || first.Reasoning.Effort != "low" || remote.auth[0] != "Bearer sk-test" {
		t.Fatalf("request %+v, auth %q", first, remote.auth[0])
	}
	offered := map[string]bool{}
	for _, tool := range first.Tools {
		offered[tool.Function.Name] = true
	}
	for _, tool := range engine.Tools() {
		delete(offered, tool.Function.Name)
	}
	if len(offered) != 0 {
		t.Fatalf("tools beyond the assistant's were offered: %v", offered)
	}
	record, _, _ := a.Core.ChatSession(ctx)
	var ref session.Ref
	if record == nil || json.Unmarshal(record.Ref, &ref) != nil || ref.Engine != harness.OpenAICompatible || ref.Home != filepath.Join(a.Core.StateDirectory(), "chat", "runtime", "openai-compatible") {
		t.Fatalf("record %+v", record)
	}
	if strings.Contains(string(record.Ref), "sk-test") {
		t.Fatal("the credential reached the stored reference")
	}

	// After a restart the conversation resumes from the library's transcript.
	a.closeChat()
	a.sessions.open = harnessChatOpener(life)
	if got := runTurn(t, a, "Still there?"); got.Message != "Still here." {
		t.Fatalf("result %+v", got)
	}
	if record, _, _ := a.Core.ChatSession(ctx); record.Opened != core.SessionResumed {
		t.Fatalf("record %+v", record)
	}
	resumed := remote.last()
	said := []string{}
	for _, m := range resumed.Messages {
		said = append(said, m.Content)
	}
	if joined := strings.Join(said, "\n"); !strings.Contains(joined, "Set up the export project") || !strings.Contains(joined, "The export project is set up.") || !strings.HasSuffix(joined, "Still there?") {
		t.Fatalf("the resumed session lost its history: %q", said)
	}
}
