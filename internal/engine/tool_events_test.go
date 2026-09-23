package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestToolObserverRecordsBeforeExecutionAndConfirmedOutcome(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "success"
		if fails {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			provider := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-one","type":"function","function":{"name":"read_state","arguments":"{}"}}]}}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Done"}}]}`))
			}))
			defer provider.Close()
			var order []string
			e, err := New(Config{Endpoint: provider.URL, Model: "fixture", OnTool: func(_ context.Context, event ToolEvent) error {
				if event.ID != "call-one" || event.Tool != "read_state" {
					t.Fatal(event)
				}
				order = append(order, event.Status)
				return nil
			}}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) {
				if !reflect.DeepEqual(order, []string{"running"}) {
					t.Fatal("tool was not observable before it ran", order)
				}
				order = append(order, "execute")
				if fails {
					return nil, errors.New("private integration diagnostic")
				}
				return map[string]bool{"ready": true}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Chat(context.Background(), Request{Message: "Check context"}); err != nil {
				t.Fatal(err)
			}
			status := "completed"
			if fails {
				status = "failed"
			}
			if !reflect.DeepEqual(order, []string{"running", "execute", status}) {
				t.Fatal(order)
			}
		})
	}
}

func TestToolObserverFailurePreventsUnrecordedAction(t *testing.T) {
	provider := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-one","type":"function","function":{"name":"read_state","arguments":"{}"}}]}}]}`))
	}))
	defer provider.Close()
	e, err := New(Config{Endpoint: provider.URL, Model: "fixture", OnTool: func(context.Context, ToolEvent) error { return errors.New("storage unavailable") }}, ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("executed without durable start event")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Chat(context.Background(), Request{Message: "Check context"}); err == nil {
		t.Fatal("observer error ignored")
	}
}
