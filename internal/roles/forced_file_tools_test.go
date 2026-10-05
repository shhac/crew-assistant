package roles

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/testutil"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// HTTP completion validates offered names before dispatch. Unlike the library's
// injected completion fixture, a forced absent tool terminates the HTTP turn
// with invalid_tool_call; it still never reaches a caller handler.
func TestAPIForcedAbsentFileToolsNeverReachCaller(t *testing.T) {
	for _, name := range []string{"read_file", "search_files", "edit_file"} {
		t.Run(name, func(t *testing.T) {
			requests, handled := 0, 0
			server := testutil.NewModelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				message := map[string]any{"role": "assistant", "content": "Finished."}
				finish := "stop"
				if requests == 1 {
					finish = "tool_calls"
					message["tool_calls"] = []any{map[string]any{"id": "forced", "type": "function", "function": map[string]any{"name": name, "arguments": "{}"}}}
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}})
			}))
			defer server.Close()
			spec := Spec{Engine: "openai-compatible", Model: "fake", WorkDir: t.TempDir(), RuntimeHome: t.TempDir(), Write: true, Prompt: "Use the supplied tools",
				Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: server.URL + "/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}},
				Tools:    []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}},
				Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) {
					handled++
					return session.ToolResult{}, nil
				})}
			n := Native{open: func(ctx context.Context, o session.Options, raw json.RawMessage) (conversation, session.Opened, error) {
				o.Workbench.Commands = nil // Isolate the HTTP surface; never a production fallback.
				return open(ctx, o, raw)
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result, err := n.Run(ctx, spec)
			if err == nil || !strings.Contains(err.Error(), "invalid_tool_call") || handled != 0 || requests != 1 || result.FailureStage != "wait" || len(result.UnavailableTools) != 3 {
				t.Fatalf("%+v %v requests=%d handled=%d", result, err, requests, handled)
			}
		})
	}
}
