package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"testing"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestHTTPEffortAndCallerTools(t *testing.T) {
	server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model     string `json:"model"`
			Effort    string `json:"reasoning_effort"`
			Tools     []Tool `json:"tools"`
			MaxTokens int    `json:"max_completion_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "api-model" || request.Effort != "high" || request.MaxTokens != 512 || len(request.Tools) != 1 || request.Tools[0].Function.Name != "worker_only" {
			t.Errorf("unexpected HTTP request: %+v", request)
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	result, _, err := Complete(context.Background(), Config{Engine: "openai-compatible", Endpoint: server.URL, Model: "api-model", Effort: "high", MaxOutputTokens: 512}, []Message{{Role: "user", Content: "hello"}}, []Tool{{Type: "function", Function: Function{Name: "worker_only"}}})
	if err != nil || result.Content != "ok" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// A provider that reports no usable accounting has not reported zero. A caller
// enforcing a budget has to be able to tell those apart.
func TestHTTPUsageRequiresCompleteNonNegativeCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage string
		want  Usage
	}{
		{"complete", `{"prompt_tokens":10,"completion_tokens":4}`, Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14, Known: true}},
		{"explicit zero stays measured", `{"prompt_tokens":0,"completion_tokens":0}`, Usage{Known: true}},
		{"absent object", "", Usage{}},
		{"empty object", `{}`, Usage{}},
		{"missing completion", `{"prompt_tokens":10}`, Usage{}},
		{"missing prompt", `{"completion_tokens":4}`, Usage{}},
		{"negative prompt", `{"prompt_tokens":-1,"completion_tokens":4}`, Usage{}},
		{"negative completion", `{"prompt_tokens":4,"completion_tokens":-1}`, Usage{}},
		{"overflowing sum", fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":2}`, math.MaxInt), Usage{}},
		// A stated total cannot rescue counts the provider did not give.
		{"total without components", `{"total_tokens":14}`, Usage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]`
			if tc.usage != "" {
				body += `,"usage":` + tc.usage
			}
			body += `}`
			server := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			_, usage, err := Complete(context.Background(), Config{Engine: "openai-compatible", Endpoint: server.URL, Model: "fixture"}, []Message{{Role: "user", Content: "Hello"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if usage != tc.want {
				t.Fatalf("got %+v want %+v", usage, tc.want)
			}
		})
	}
}
