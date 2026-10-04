package roles

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/bundledskills"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func TestAPIProvidedSkillCoexistsWithDaemonToolsAndRejectsWrites(t *testing.T) {
	set, digest, err := bundledskills.Prepare(t.TempDir(), "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(set.Provided[0].Dir, "SKILL.md")
	before, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	requests, handled := 0, 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		names := []string{}
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		if !slices.Contains(names, "load_skill") || !slices.Contains(names, "read_task") || slices.Contains(names, "run_skill_script") {
			t.Error("wrong tools", names)
		}
		requests++
		name, args := "", ""
		switch requests {
		case 1:
			name, args = "load_skill", `{"skill":"sprite-atlas"}`
		case 2:
			if !strings.Contains(string(req.Messages[len(req.Messages)-1].Content), "canonical base image") {
				t.Error("missing delivered skill")
			}
			name = "write_file"
			raw, _ := json.Marshal(map[string]string{"path": skillPath, "content": "overwrite"})
			args = string(raw)
		case 3:
			if !strings.Contains(string(req.Messages[len(req.Messages)-1].Content), "write_file error: file_path_invalid") {
				t.Error("skill write not refused", string(req.Messages[len(req.Messages)-1].Content))
			}
			name, args = "read_task", `{}`
		case 4:
			if !strings.Contains(string(req.Messages[len(req.Messages)-1].Content), "task record") {
				t.Error("missing daemon tool reply")
			}
		default:
			t.Error("unexpected extra request")
		}
		message := map[string]any{"role": "assistant", "content": "Reviewed."}
		finish := "stop"
		if name != "" {
			finish = "tool_calls"
			message["tool_calls"] = []any{map[string]any{"id": name, "type": "function", "function": map[string]any{"name": name, "arguments": args}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}})
	})
	// This required behavioral fixture runs in the daemon-hosted localhost
	// check. Failure to admit it is a failure, never hidden behind a skip.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("required bundled-skill fixture needs the daemon-hosted localhost check: %v", err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	server.Start()
	defer server.Close()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: t.TempDir(), RuntimeHome: home, Write: true, Skills: set, Instructions: bundledskills.Fingerprint(digest), Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: server.URL + "/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}, Prompt: "Review the strip", Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(_ context.Context, call session.ToolCall) (session.ToolResult, error) {
		handled++
		if call.Name != "read_task" {
			t.Error("skill call routed to daemon", call.Name)
		}
		return session.ToolResult{Content: "task record"}, nil
	})}
	n := Native{open: func(ctx context.Context, o session.Options, raw json.RawMessage) (conversation, session.Opened, error) {
		o.Workbench.Commands = nil
		return open(ctx, o, raw)
	}}
	result, err := n.Run(context.Background(), spec)
	if err != nil || result.Text != "Reviewed." || requests != 4 || handled != 1 {
		t.Fatal(result, requests, handled, err)
	}
	after, err := os.ReadFile(skillPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("skill changed", err)
	}
}
