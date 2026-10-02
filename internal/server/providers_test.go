package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

// The dashboard's provider picker lists the saved providers, the single API
// setting first, and a saved change shows at once.
func TestProvidersEndpointListsTheSavedProviders(t *testing.T) {
	a, call := ownerApp(t)
	var got []providerChoice
	if w := call("GET", "/api/providers", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(got) != 1 || got[0] != (providerChoice{ID: "openai-compatible", Label: "api.openai.com"}) {
		t.Fatalf("legacy only %+v", got)
	}
	cfg := a.Config()
	cfg.Engines.Providers = []config.Provider{{ID: "openrouter", Name: "OpenRouter", HTTPEngine: config.HTTPEngine{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY"}}}
	body, _ := json.Marshal(cfg)
	if w := call("PUT", "/api/config", string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("GET", "/api/providers", "")
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 2 || got[1] != (providerChoice{ID: "openrouter", Label: "OpenRouter"}) {
		t.Fatalf("named %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "OPENROUTER_API_KEY") {
		t.Fatal("the picker needs no key names", w.Body.String())
	}
}

// A team member can't pick an API provider while the harness offers it no
// sandboxed session, and the dashboard is told why.
func TestEngineChoicesSayWhyRolesCantUseAnAPI(t *testing.T) {
	for _, choice := range engineChoices() {
		switch choice.Engine {
		case "openai-compatible":
			ok, reason := config.RoleSupport(choice.Engine)
			if choice.Roles != ok || choice.RolesReason != reason || !choice.Assistant || !choice.Small || choice.Browser {
				t.Fatalf("%+v", choice)
			}
		case "claude", "codex":
			if !choice.Roles || choice.RolesReason != "" {
				t.Fatalf("%+v", choice)
			}
		}
	}
}
