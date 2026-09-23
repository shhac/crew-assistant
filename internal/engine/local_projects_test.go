package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestLocalProjectGuidanceAndToolsReachModel(t *testing.T) {
	var calls, creations atomic.Int32
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []Message `json:"messages"`
			Tools    []Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(request.Messages) == 0 || request.Messages[0].Role != "system" {
			t.Error("missing system guidance")
			w.WriteHeader(400)
			return
		}
		guidance := request.Messages[0].Content
		for _, boundary := range []string{"state is the project registry", "connections are optional resources", "A configured account does not establish its relevance", "unless the owner explicitly links that resource or asks to use it"} {
			if !strings.Contains(guidance, boundary) {
				t.Errorf("model missing resource boundary %q", boundary)
			}
		}
		if calls.Add(1) == 1 {
			foundCreate, foundQuery := false, false
			for _, tool := range request.Tools {
				switch tool.Function.Name {
				case "create_project":
					foundCreate = true
					if !strings.Contains(tool.Function.Description, "No Linear issue, external tracker, or connection is required") {
						t.Error("local project tool implies an external dependency")
					}
					for _, key := range tool.Function.Parameters["required"].([]any) {
						if key == "source_id" || key == "connection_id" || key == "profile" {
							t.Errorf("local project requires external resource %v", key)
						}
					}
				case "query_connection":
					foundQuery = true
					if !strings.Contains(tool.Function.Description, "Do not query a work account for a personal project") {
						t.Error("connection tool missing account context boundary")
					}
				}
			}
			if !foundCreate || !foundQuery {
				t.Error("missing coordination tools")
			}
			call := ToolCall{ID: "local-create", Type: "function"}
			call.Function.Name = "create_project"
			call.Function.Arguments = `{"title":"Personal project","objective":"","acceptance_criteria":[],"directories":null}`
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": Message{Role: "assistant", ToolCalls: []ToolCall{call}}}}})
			return
		}
		if request.Messages[len(request.Messages)-1].ToolCallID != "local-create" {
			t.Error("local project evidence not returned to model")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The project is tracked locally."}}]}`))
	}))
	defer server.Close()
	e, err := New(Config{Endpoint: server.URL, Model: "synthetic"}, ExecutorFunc(func(_ context.Context, name string, raw json.RawMessage) (any, error) {
		if name != "create_project" {
			t.Errorf("unexpected integration dependency: %s", name)
		}
		creations.Add(1)
		var args CreateProjectArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		return map[string]string{"id": "local-project", "title": args.Title}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Chat(context.Background(), Request{Message: "Track this personal project locally; my Linear account is for work.", Context: json.RawMessage(`{"connections":[{"id":"work","tool":"lin","profiles":["employer"]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || creations.Load() != 1 || len(result.Actions) != 1 {
		t.Fatalf("local project flow: calls=%d creations=%d result=%+v", calls.Load(), creations.Load(), result)
	}
}
