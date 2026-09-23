package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

func TestWorkerContextIncludesConfiguredModelAndActualAuthority(t *testing.T) {
	a, ag := usageRuntimeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("context contacted broker") })
	cfg := a.Config()
	model := cfg.WorkerModel
	model.CodexHome = "/private/login-must-not-reach-model"
	model.APIKeyEnv = "PRIVATE_SECRET_REFERENCE"
	model.Engine = "claude"
	model.Model = "claude-opus-5"
	model.Effort = "high"
	cfg.Workers[0].ModelProfile = &model
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, restricted := range []bool{false, true} {
		if restricted {
			a.SetNoDispatch()
		}
		raw, _, err := a.context(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var context struct {
			Profiles  []WorkerDetail     `json:"worker_profiles"`
			Authority ExecutionAuthority `json:"execution_authority"`
		}
		if err = json.Unmarshal(raw, &context); err != nil {
			t.Fatal(err)
		}
		if len(context.Profiles) != 1 || context.Profiles[0].Model == nil || context.Profiles[0].Model.Model != "claude-opus-5" || context.Authority.CanCommission == restricted {
			t.Fatal(string(raw))
		}
		for _, secret := range []string{model.CodexHome, model.APIKeyEnv} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("private worker settings leaked")
			}
		}
	}
	raw, _ := json.Marshal(engine.DelegateArgs{ProjectID: ag.ProjectID, WorkerProfile: ag.ProfileID, Role: "worker", Objective: "Bounded task", AcceptanceCriteria: []string{"Tests pass"}})
	if _, err := a.Execute(context.Background(), "delegate", raw); err == nil {
		t.Fatal("disabled dispatch accepted new commission")
	}
}

func TestProceedCanCommissionPreparedWorkerDespiteOldRefusal(t *testing.T) {
	ctx := context.Background()
	starts := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		starts++
		var in worker.StartRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		writeRun(w, worker.Run{ID: "synthetic-run", DispatchKey: in.DispatchKey, Status: "running", Summary: "Reviewing the agreed outcome", UpdatedAt: time.Now()})
	}))
	defer broker.Close()
	a := testApp(t)
	client, err := worker.New(worker.Config{Endpoint: broker.URL, Capabilities: []string{"implement", "review"}})
	if err != nil {
		t.Fatal(err)
	}
	a.managed = quotaManaged{client}
	a.workerPreflight = func(context.Context, config.Model) error { return nil }
	a.workerUsage.Inspect = func(context.Context, session.Options) (session.Inspection, error) {
		return session.Inspection{Quota: quotaFixture(10)}, nil
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Assistant project", Directories: []string{dir}, AcceptanceCriteria: "A passing workflow test"})
	if err != nil {
		t.Fatal(err)
	}
	prepare, _ := json.Marshal(map[string]string{"project_id": project.ID, "workspace": dir})
	prepared, err := a.Execute(ctx, "prepare_worker", prepare)
	if err != nil {
		t.Fatal(err)
	}
	profile := prepared.(WorkerDetail)
	if profile.Model == nil || profile.Model.Model != a.Config().WorkerModel.Model {
		t.Fatal("preparation lost model details")
	}
	a.Core.AddMessage(ctx, "assistant", "This session cannot commission workers.")
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []engine.Message `json:"messages"`
			Tools    []engine.Tool    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		calls++
		if calls == 1 {
			if !strings.Contains(request.Messages[0].Content, "Current execution_authority and tool results take precedence") {
				t.Error("historical refusal was not corrected by current instructions")
			}
			if !strings.Contains(request.Messages[1].Content, `"can_commission":true`) || !strings.Contains(request.Messages[1].Content, profile.Model.Model) {
				t.Error("model lacks current authority/model evidence")
			}
			args, _ := json.Marshal(engine.DelegateArgs{ProjectID: project.ID, WorkerProfile: profile.ID, Role: "worker", Objective: "Prove the workflow", AcceptanceCriteria: []string{"A passing workflow test"}})
			call := engine.ToolCall{ID: "commission", Type: "function"}
			call.Function.Name = "delegate"
			call.Function.Arguments = string(args)
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": engine.Message{Role: "assistant", ToolCalls: []engine.ToolCall{call}}}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": engine.Message{Role: "assistant", Content: "The worker is queued for the agreed outcome."}}}})
	}))
	defer model.Close()
	cfg := a.Config()
	cfg.Model.BaseURL = model.URL
	cfg.Model.Model = "synthetic-model"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	result, err := a.Chat(ctx, "Proceed with the agreed workflow test.")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(result.Actions) != 1 || !result.Actions[0].Success {
		t.Fatalf("failed commission: %+v", result)
	}
	snapshot, _ := a.Core.Snapshot(ctx)
	if len(snapshot.Agents) != 1 || snapshot.Agents[0].Status != "queued" {
		t.Fatal("prepared profile was not commissioned")
	}
	if err := a.tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatal("queued worker did not dispatch", starts)
	}
}

func TestWorkerModelToolsConfigureActualSelectionAndRetainLogin(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Local project", Directories: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.WorkerModel.ClaudeHome = filepath.Join(t.TempDir(), "selected-login")
	cfg.Workers = []config.Worker{{ID: "local", Name: "Project worker", ProjectID: p.ID, Workspace: dir, Managed: true, Capabilities: []string{"implement"}}}
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	discoveries := 0
	a.workerDiscover = func(_ context.Context, got engine.Config) ([]engine.ModelOption, error) {
		discoveries++
		if got.Engine != "claude" || got.ClaudeHome != cfg.WorkerModel.ClaudeHome {
			t.Fatal("discovery used wrong login")
		}
		return []engine.ModelOption{{ID: "claude-opus-5"}}, nil
	}
	discovered, err := a.Execute(ctx, "list_worker_models", json.RawMessage(`{"worker_profile":"local","engine":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(discovered)
	if discoveries != 1 || !strings.Contains(string(encoded), "claude-opus-5") || strings.Contains(string(encoded), cfg.WorkerModel.ClaudeHome) {
		t.Fatal("bad discovery result", string(encoded))
	}
	a.workerPreflight = func(_ context.Context, got config.Model) error {
		if got.Engine != "claude" || got.Model != "claude-opus-5" || got.Effort != "high" || got.ClaudeHome != cfg.WorkerModel.ClaudeHome {
			t.Fatal("configuration did not verify selected model with saved login")
		}
		return nil
	}
	request, _ := json.Marshal(map[string]string{"project_id": p.ID, "worker_profile": "local", "name": "Project reviewer", "engine": "claude", "model": "claude-opus-5", "effort": "high"})
	result, err := a.Execute(ctx, "configure_worker", request)
	if err != nil {
		t.Fatal(err)
	}
	detail := result.(WorkerDetail)
	if detail.Model.Model != "claude-opus-5" || detail.Model.Effort != "high" || detail.Name != "Project reviewer" || detail.ModelStatus != "configured" {
		t.Fatal("incorrect configured result", detail)
	}
	saved := a.Config().Workers[0]
	if saved.ModelProfile == nil || saved.ModelProfile.ClaudeHome != cfg.WorkerModel.ClaudeHome || saved.Workspace != dir || len(saved.Capabilities) != 1 {
		t.Fatal("configuration changed login or scope")
	}
	if _, err := (projectExecutor{app: a, projectID: p.ID}).Execute(ctx, "configure_worker", request); err == nil {
		t.Fatal("descendant changed model configuration")
	}
}
