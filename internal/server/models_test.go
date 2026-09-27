package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/catalog"
)

func TestModelEndpointUsesSavedProfileAndCaches(t *testing.T) {
	cfg := config.Default()
	cfg.Engines.Codex.Home = "/test/assistant-login"
	a := app.New(nil, cfg, filepath.Join(t.TempDir(), "config.json"), app.Options{})
	calls := 0
	handler := modelHandler(a, func(_ context.Context, p harness.Provider) ([]catalog.Model, error) {
		calls++
		if p.Engine != harness.Codex || p.CLI.Home != cfg.Engines.Codex.Home {
			t.Fatal("wrong profile", p)
		}
		return []catalog.Model{{ID: "test", Name: "Test model", DefaultEffort: "high"}}, nil
	})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models?profile=assistant", nil))
		var result modelCatalog
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Available || result.Profile != "assistant" || result.Current.Model != cfg.AssistantHarness().Model || result.Default.Model != config.DefaultProfile().Model.Model || len(result.Models) != 1 {
			t.Fatal(w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("repeated discovery", calls)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models?profile=unknown", nil))
	if w.Code != 400 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
}

func TestModelEndpointFailureDoesNotInventModels(t *testing.T) {
	cfg := config.Default()
	a := app.New(nil, cfg, filepath.Join(t.TempDir(), "config.json"), app.Options{})
	handler := modelHandler(a, func(context.Context, harness.Provider) ([]catalog.Model, error) {
		return nil, errors.New("secret-provider-diagnostic")
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models", nil))
	var result modelCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Available || len(result.Models) != 0 || result.Current.Model != cfg.AssistantHarness().Model || strings.Contains(w.Body.String(), "secret-provider") {
		t.Fatal(w.Body.String())
	}
}

func TestDemoModelDiscoveryNeverStartsProcess(t *testing.T) {
	a := app.New(nil, config.Default(), "", app.Options{Demo: true})
	handler := modelHandler(a, func(context.Context, harness.Provider) ([]catalog.Model, error) {
		t.Fatal("demo started discovery")
		return nil, nil
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal(w.Body.String())
	}
}

func TestModelEndpointCanPreviewClaudeBeforeSavingEngine(t *testing.T) {
	cfg := config.Default()
	cfg.Engines.Claude.Home = "/test/shared-claude"
	a := app.New(nil, cfg, "", app.Options{})
	handler := modelHandler(a, func(_ context.Context, p harness.Provider) ([]catalog.Model, error) {
		if p.Engine != harness.Claude || p.CLI.Home != cfg.Engines.Claude.Home {
			t.Fatal(p)
		}
		return []catalog.Model{{ID: "opus", Resolved: "claude-opus-5-5", Name: "Opus"}}, nil
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models?profile=assistant&engine=claude", nil))
	var result modelCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Engine != "claude" || !result.Available || a.Config().AssistantHarness().Engine != "codex" {
		t.Fatal(w.Body.String())
	}
	// The alias is what is chosen; the model it selects today is shown beside it.
	if !strings.Contains(w.Body.String(), `"id":"opus","resolved":"claude-opus-5-5"`) {
		t.Fatal(w.Body.String())
	}
}

// An endpoint lists its models too, reached at its base URL with its key,
// and a model without listed efforts still shows an empty list of them.
func TestModelEndpointListsAnAPIsModels(t *testing.T) {
	cfg := config.Default()
	cfg.Engines.OpenAICompatible = config.HTTPEngine{BaseURL: "https://gateway.example.test/v1", APIKeyEnv: "GATEWAY_TEST_KEY"}
	a := app.New(nil, cfg, "", app.Options{})
	handler := modelHandler(a, func(_ context.Context, p harness.Provider) ([]catalog.Model, error) {
		if p.Engine != harness.OpenAICompatible || p.API.BaseURL != cfg.Engines.OpenAICompatible.BaseURL || p.API.Credentials == nil {
			t.Fatal(p)
		}
		return []catalog.Model{{ID: "xai/grok-4", Name: "xai/grok-4"}}, nil
	})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models?profile=assistant&engine=openai-compatible", nil))
	if !strings.Contains(w.Body.String(), `"efforts":[]`) || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/models?profile=assistant&engine=unknown", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestModelDiscoveryRequiresOwnerAuthentication(t *testing.T) {
	root := t.TempDir()
	auth, err := NewAuth(root, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(nil, config.Default(), "", app.Options{})
	handler := auth.Middleware(modelHandler(a, func(context.Context, harness.Provider) ([]catalog.Model, error) {
		t.Fatal("unauthenticated discovery")
		return nil, nil
	}))
	r := httptest.NewRequest("GET", "http://127.0.0.1:8340/api/models", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
}
