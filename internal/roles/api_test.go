package roles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/testutil"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// The harness keeps its HTTP transport private. Use its local fake-provider
// pattern, refusing any address except this fixture's loopback listener.
func TestAPIReviewerReadsAndCallsDaemonToolsAndResumesAfterSettingsChange(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	handled := 0
	var names [][]string
	server := testutil.NewModelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var offered []string
		for _, tool := range request.Tools {
			offered = append(offered, tool.Function.Name)
		}
		names = append(names, offered)
		requests++
		message := map[string]any{"role": "assistant", "content": "Reviewed."}
		finish := "stop"
		calls := map[int]struct{ name, args string }{
			1: {"list_files", "{}"},
			2: {"read_task", "{}"},
		}
		if requests == 2 {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || !strings.Contains(string(last.Content), "artifact.txt") {
				t.Error("workbench list result missing", string(last.Content))
			}
		}
		if requests == 4 {
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "user" || !strings.Contains(string(last.Content), "Fresh task context") {
				t.Error("fresh task context missing", request.Messages)
			}
			for _, message := range request.Messages[:len(request.Messages)-1] {
				if message.Role != "system" {
					t.Error("incompatible session retained its conversation", request.Messages)
				}
			}
			if !strings.Contains(string(request.Messages[0].Content), "read_file, search_files and edit_file are unavailable") {
				t.Error("library absence notice missing", request.Messages)
			}
		}
		if call, ok := calls[requests]; ok {
			message["tool_calls"] = []any{map[string]any{"id": call.name, "type": "function", "function": map[string]any{"name": call.name, "arguments": call.args}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}})
	}))
	defer server.Close()
	work, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "artifact.txt"), []byte("synthetic artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Engine: "openai-compatible", Model: "fake-tools-model", WorkDir: work, RuntimeHome: home, Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: server.URL + "/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}, Prompt: "Review the artifact", Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(_ context.Context, call session.ToolCall) (session.ToolResult, error) {
		if call.Name != "read_task" {
			t.Error("workbench call reached daemon handler", call.Name)
		}
		handled++
		return session.ToolResult{Content: "task record"}, nil
	})}
	// Exercise the read workbench independently of the OS command proof, as
	// the library's read-workbench tests do. Production never removes commands
	// after a failed proof; TestAPICommandProof covers that path separately.
	n := Native{open: func(ctx context.Context, o session.Options, ref json.RawMessage) (conversation, session.Opened, error) {
		o.Workbench.Commands = nil
		return open(ctx, o, ref)
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	first, err := n.Run(ctx, spec)
	if err != nil || first.Text != "Reviewed." || handled != 1 || len(first.UnavailableTools) != 2 {
		t.Fatalf("%+v %v handled=%d", first, err, handled)
	}
	var ref session.Ref
	// The library names the workspace by its canonical path, which differs
	// from a temporary directory's on macOS (/var is /private/var).
	canonical, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(first.Session, &ref) != nil || ref.ConfigHash == "" || ref.WorkDir != canonical {
		t.Fatal("stored reference lost workbench identity", string(first.Session))
	}
	spec.Resume, spec.Write, spec.FreshPrompt = first.Session, true, "Fresh task context"
	second, err := n.Run(ctx, spec)
	if err != nil || string(second.Session) == string(first.Session) || len(second.UnavailableTools) != 3 {
		t.Fatalf("changed settings did not open fresh: %+v %v", second, err)
	}
	for i, offered := range names {
		for _, name := range []string{"list_files", "read_task"} {
			if !slices.Contains(offered, name) {
				t.Errorf("request %d missing %s", i, name)
			}
		}
		for _, name := range []string{"read_file", "search_files", "edit_file"} {
			if slices.Contains(offered, name) {
				t.Errorf("disabled tool offered: %s", name)
			}
		}
		if slices.Contains(offered, "write_file") != (i >= 3) {
			t.Errorf("request %d write surface: %v", i, offered)
		}
	}
}

func TestAPICommandProof(t *testing.T) {
	if !harness.Support(harness.OpenAICompatible, harness.Session, harness.Sandbox).Usable() {
		if options(Spec{Engine: "openai-compatible"}).Workbench.Commands != nil {
			t.Fatal("commands on unsupported platform")
		}
		return
	}
	spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: t.TempDir(), RuntimeHome: t.TempDir(), Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}}
	spec.Env = []string{"GOCACHE=/cache/go", "GOMODCACHE=/modules", "GOPROXY=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "npm_config_cache=/cache/npm", "XDG_CACHE_HOME=/cache/xdg", "npm_config_update_notifier=false", "CI=1", "PORT=41234", "TMPDIR=/private", "HOME=/private"}
	spec.Tools = []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}
	spec.Handler = session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) { return session.ToolResult{}, nil })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := os.Chmod(spec.RuntimeHome, 0700); err != nil {
		t.Fatal(err)
	}
	s, _, err := open(ctx, options(spec), nil)
	if err == nil {
		s.Close()
		if _, releaseErr := s.Release(ctx); releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if err := os.WriteFile(filepath.Join(spec.WorkDir, "artifact.txt"), []byte("command-backed artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		requests := 0
		server := testutil.NewModelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Messages []struct {
					Role    string
					Content json.RawMessage
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			requests++
			message := map[string]any{"role": "assistant", "content": "Read with commands."}
			finish := "stop"
			if requests == 1 {
				message["tool_calls"] = []any{map[string]any{"id": "command", "type": "function", "function": map[string]any{"name": "run_command", "arguments": `{"command":"cat artifact.txt"}`}}}
				finish = "tool_calls"
			} else {
				last := request.Messages[len(request.Messages)-1]
				if last.Role != "tool" || !strings.Contains(string(last.Content), "command-backed artifact") {
					t.Error("command did not read artifact", string(last.Content))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}})
		}))
		defer server.Close()
		spec.Provider.API.BaseURL = server.URL + "/v1"
		spec.Env = nil
		spec.Prompt = "Read artifact.txt with run_command"
		out, runErr := (Native{}).Run(ctx, spec)
		if runErr != nil || out.Text != "Read with commands." || requests != 2 || len(out.UnavailableTools) != 2 {
			t.Fatalf("%+v %v requests=%d", out, runErr, requests)
		}
		for _, tool := range out.UnavailableTools {
			if (tool.Name != "read_file" && tool.Name != "search_files") || tool.Capability.Usable() || tool.Capability.Reason == "" {
				t.Fatal("unexpected unavailable tool", tool)
			}
		}
		return
	}
	var capability *session.CapabilityError
	if !errors.As(err, &capability) || capability.Phase != session.BeforeLaunch || !Permanent(err) {
		t.Fatalf("command proof did not produce a permanent prelaunch refusal: %v", err)
	}
	entries, readErr := os.ReadDir(spec.RuntimeHome)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed proof wrote a transcript: %v %v", entries, readErr)
	}
}

// Opening an API read workbench uses no sockets or inference. This checks
// the real library's stored digest and fresh fallback even in a sandbox
// that refuses the local fake-provider fixture.
func TestAPIWorkbenchReferenceChangesOpenFreshWithoutInference(t *testing.T) {
	work, home := t.TempDir(), t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: work, RuntimeHome: home, Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}, Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) { return session.ToolResult{}, nil })}
	o := options(spec)
	o.Workbench.Commands = nil // Read-only library fixture, never a production fallback.
	started, err := session.Start(context.Background(), o)
	if err != nil {
		t.Fatal("workbench Start refused disabled content tools:", err)
	}
	for _, tool := range started.Capabilities().WorkbenchTools {
		if tool.Name == "read_file" || tool.Name == "search_files" {
			if tool.Capability.Usable() || tool.Capability.Reason != "workbench file tools are off until their workspace check is verified" {
				t.Fatal(tool)
			}
		}
	}
	if _, err := started.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, resumed, err := open(context.Background(), o, nil)
	if err != nil || resumed.Resumed {
		t.Fatal(resumed, err)
	}
	ref := s.Ref()
	// Close gives the conversation up only once its calls have returned;
	// Release waits for that, as Native.Run does, before it is opened again.
	if _, err := s.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ := json.Marshal(ref)
	s, resumed, err = open(context.Background(), o, stored)
	if err != nil || !resumed.Resumed || s.Ref().ConfigHash != ref.ConfigHash {
		t.Fatal("unchanged workbench failed to resume", resumed, err)
	}
	if _, err := s.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, saved := range []struct {
		raw    json.RawMessage
		reason string
	}{
		{json.RawMessage("invalid"), session.FreshIncompatible},
		{func() json.RawMessage {
			gone := ref
			gone.ID = "00000000-0000-0000-0000-000000000001"
			raw, _ := json.Marshal(gone)
			return raw
		}(), session.FreshUnavailable},
	} {
		fresh, opened, err := open(context.Background(), o, saved.raw)
		if err != nil || opened.Resumed || opened.Fresh != saved.reason {
			t.Fatalf("actual opening %+v: %v", opened, err)
		}
		if _, err := fresh.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []string{"write", "model", "provider", "provider identity", "workspace", "instructions"} {
		t.Run(change, func(t *testing.T) {
			changed := o
			wb := *o.Workbench
			changed.Workbench = &wb
			switch change {
			case "write":
				changed.Workbench.Write = true
			case "model":
				changed.Model = "other"
			case "provider":
				changed.Provider.API.BaseURL = "http://127.0.0.1:2/v1"
			case "provider identity":
				changed.AccountIdentity = "other-provider:OTHER_KEY_SOURCE"
			case "workspace":
				changed.WorkDir = t.TempDir()
			case "instructions":
				changed.Instructions = session.Instructions{Mode: session.Append, Text: "Read and search through run_command; file tools are unavailable."}
			}
			if _, err := session.Resume(context.Background(), changed, ref); !errors.Is(err, session.ErrIncompatibleResume) {
				t.Fatal("changed reference resumed", err)
			}
			// The harness Open itself may start fresh on a mismatch. Native.Run
			// also handles an opener returning the refusal, tested separately.
			s, resumed, err := open(context.Background(), changed, stored)
			if err != nil || resumed.Resumed || resumed.Fresh != session.FreshIncompatible {
				t.Fatal("settings change did not start fresh", resumed, err)
			}

			defer s.Close()
			if s.Ref().ID == ref.ID || (s.Ref().ConfigHash == ref.ConfigHash && s.Ref().AccountIdentity == ref.AccountIdentity) {
				t.Fatal("old reference reused", s.Ref())
			}
		})
	}
}

func TestAPIRefusedCallerEnvironmentStopsBeforeLaunch(t *testing.T) {
	if !harness.Support(harness.OpenAICompatible, harness.Session, harness.Sandbox).Usable() {
		if got := CommandEnv([]string{"API_TOKEN=synthetic-value"}, nil); !slices.Contains(got, "API_TOKEN=synthetic-value") {
			t.Fatal("refused caller setting silently dropped")
		}
		return
	}
	for _, key := range []string{"API_TOKEN", "LD_PRELOAD", "BASH_ENV", "AGENT_HARNESS_MARKER"} {
		t.Run(key, func(t *testing.T) {
			spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: t.TempDir(), RuntimeHome: t.TempDir(), Env: []string{"GOPROXY=off", key + "=synthetic-value"}, Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}, Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) {
				t.Fatal("tool ran with refused environment")
				return session.ToolResult{}, nil
			})}
			_, err := (Native{}).Run(context.Background(), spec)
			var refused *session.UnsupportedError
			if !errors.As(err, &refused) || refused.Code != session.RefusedConflict || !Permanent(err) || strings.Contains(err.Error(), "synthetic-value") {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(spec.RuntimeHome)
			if err != nil || len(entries) != 0 {
				t.Fatal("refused environment wrote transcript", entries, err)
			}
		})
	}
}
