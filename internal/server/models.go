package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

type modelCatalog struct {
	Profile   string               `json:"profile"`
	Engine    string               `json:"engine"`
	Available bool                 `json:"available"`
	Detail    string               `json:"detail"`
	Models    []engine.ModelOption `json:"models"`
	Current   modelSelection       `json:"current"`
	Default   modelSelection       `json:"default"`
}
type modelSelection struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}
type modelDiscovery func(context.Context, engine.Config) ([]engine.ModelOption, error)

func registerModels(mux *http.ServeMux, a *app.App) {
	mux.Handle("GET /api/models", modelHandler(a, engine.DiscoverModels))
}

func modelHandler(a *app.App, discover modelDiscovery) http.Handler {
	// Serialize discovery and cache each saved profile briefly: repeated renders
	// must not create a fresh account-connected subprocess every time.
	var mu sync.Mutex
	type cached struct {
		value   []engine.ModelOption
		detail  string
		expires time.Time
	}
	cache := make(map[config.Harness]cached)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile := r.URL.Query().Get("profile")
		if profile == "" {
			profile = "assistant"
		}
		if profile != "assistant" {
			http.Error(w, "unknown model profile", http.StatusBadRequest)
			return
		}
		cfg := a.Config()
		// The cache is keyed by the CLI and login too, so changing either
		// discovers again.
		selected, defaults := cfg.AssistantHarness(), config.Default().Model
		result := modelCatalog{Profile: profile, Engine: selected.Engine, Models: []engine.ModelOption{}, Current: modelSelection{selected.Model, selected.Effort}, Default: modelSelection{defaults.Model, defaults.Effort}}
		if a.Demo {
			result.Detail = "Model discovery is unavailable in the demo."
			respond(w, 200, result)
			return
		}
		if preview := r.URL.Query().Get("engine"); preview != "" {
			if _, ok := cfg.Engines.CLI(preview); !ok {
				http.Error(w, "unknown model engine", http.StatusBadRequest)
				return
			}
			selected = cfg.Harness(preview, selected.Model, selected.Effort)
			result.Engine = preview
		}
		if _, ok := cfg.Engines.CLI(selected.Engine); !ok {
			result.Detail = "Use advanced settings for this provider's model. The saved selection is unchanged."
			respond(w, 200, result)
			return
		}
		mu.Lock()
		entry, found := cache[selected]
		if !found || time.Now().After(entry.expires) {
			// Only the CLI and its login decide what is offered, which is
			// also what the cache is keyed by.
			ec := app.EngineConfig(selected)
			options, err := discover(r.Context(), engine.Config{Engine: ec.Engine, CodexHome: ec.CodexHome, CodexBin: ec.CodexBin, ClaudeHome: ec.ClaudeHome, ClaudeBin: ec.ClaudeBin})
			entry = cached{value: options, expires: time.Now().Add(time.Minute)}
			if err != nil {
				entry.detail = "Could not discover models. Check the selected CLI login and installation, then retry. Your saved selection is unchanged."
				entry.expires = time.Now().Add(5 * time.Second)
			}
			// Bound the cache across per-worker profiles and configuration edits.
			if len(cache) >= 32 {
				clear(cache)
			}
			cache[selected] = entry
		}
		mu.Unlock()
		result.Available = entry.detail == ""
		result.Detail = entry.detail
		if entry.value != nil {
			result.Models = entry.value
		}
		if result.Available {
			result.Detail = "Models and reasoning options reported by your selected CLI installation."
		}
		respond(w, 200, result)
	})
}
