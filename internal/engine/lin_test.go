package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestLinToolIsOnlyOfferedWhenConfigured(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		found := false
		for _, tool := range Tools(enabled) {
			found = found || tool.Function.Name == "lin"
		}
		if found != enabled {
			t.Fatal("wrong tool set")
		}
		if (CheckToolCall("lin", json.RawMessage(`{}`), enabled) == nil) != enabled {
			t.Fatal("wrong tool admission")
		}
		var call ToolCall
		call.ID = "1"
		call.Type = "function"
		call.Function.Name = "lin"
		call.Function.Arguments = `{}`
		if _, err := validToolCall(call, map[string]bool{}, enabled); (err == nil) != enabled {
			t.Fatal("wrong stateless admission")
		}
		server := testutil.NewModelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Tools []Tool `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			present := false
			for _, tool := range request.Tools {
				present = present || tool.Function.Name == "lin"
			}
			if present != enabled {
				t.Error("stateless request has wrong tool set")
			}
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		}))
		e, err := New(Config{Provider: api(server.URL), Model: "test", Lin: enabled}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) { return nil, nil }))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = e.complete(context.Background(), []Message{{Role: "user", Content: "hello"}}); err != nil {
			t.Fatal(err)
		}
		server.Close()
	}
}
