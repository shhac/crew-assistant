package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestWorkerControlAPIKeepsUncertainOperationIdAndPrivateConversation(t *testing.T) {
	var posts atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts.Add(1); http.Error(w, "interrupted", 500) }))
	defer broker.Close()
	cfg := config.Default()
	cfg.Workers = []config.Worker{{ID: "fake", Endpoint: broker.URL, Capabilities: []string{"implement"}}}
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	ctx := context.Background()
	p, err := service.CreateProject(ctx, core.ProjectInput{Title: "Fixture", AcceptanceCriteria: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := service.Delegate(ctx, core.DelegateInput{ProjectID: p.ID, ProfileID: "fake", Role: "worker", Task: "Check fixture", AcceptanceCriteria: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = service.BeginDispatch(ctx, ag.ID)
	_ = service.MarkDispatched(ctx, ag.ID, "external")
	_ = service.SetAgentControlCapabilities(ctx, ag.ID, []string{"pause"})
	a := app.New(service, cfg, filepath.Join(t.TempDir(), "config.json"), false)
	mux := http.NewServeMux()
	registerAgentControls(mux, a)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	route := "/api/agents/" + ag.ID
	payload := `{"action":"pause","operation_id":"stable-pause"}`
	response := call("POST", route+"/control", payload)
	if response.Code != 503 {
		t.Fatal("uncertain write misreported as definite rejection", response.Code, response.Body.String())
	}
	response = call("POST", route+"/control", payload)
	if response.Code != 503 || posts.Load() != 1 {
		t.Fatal("retry duplicated pause or claimed confirmation", posts.Load(), response.Code, response.Body.String())
	}
	response = call("POST", route+"/messages", `{"message_id":"owner-direction","message":"Keep the fixture concise"}`)
	if response.Code != 201 || posts.Load() != 1 {
		t.Fatal("stored owner direction woke held worker", response.Code, posts.Load())
	}
	response = call("GET", route+"/conversation", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Owner requested pause") {
		t.Fatal("missing public control record", response.Code, response.Body.String())
	}
	response = call("GET", route+"/conversation?after=-1", "")
	if response.Code != 400 {
		t.Fatal("invalid pagination accepted")
	}
	response = call("POST", route+"/control", `{"action":"pause","operation_id":"extra","credential":"forbidden"}`)
	if response.Code != 400 {
		t.Fatal("unexpected control fields accepted")
	}
}
