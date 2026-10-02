package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/catalog"
)

type modelCatalog struct {
	Profile string `json:"profile"`
	Engine  string `json:"engine"`
	// Provider is the API provider listed, for a model on another API.
	Provider  string         `json:"provider,omitempty"`
	Available bool           `json:"available"`
	Detail    string         `json:"detail"`
	Models    []listedModel  `json:"models"`
	Current   modelSelection `json:"current"`
	Default   modelSelection `json:"default"`
}

// listedModel is a model as the picker shows it, marked when it costs
// nothing to use.
type listedModel struct {
	catalog.Model
	// Free is a model the endpoint names as free, as OpenRouter's :free
	// variants are; they come with tight rate limits.
	Free bool `json:"free,omitempty"`
}
type modelSelection struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}
type modelDiscovery func(context.Context, harness.Provider) ([]catalog.Model, error)

func modelHandler(a *app.App, discover modelDiscovery) http.Handler {
	lookup := newModelLookup(discover)
	return modelHandlerWithLookup(a, lookup)
}

type cachedModels struct {
	value   []catalog.Model
	detail  string
	expires time.Time
}
type modelLookup struct {
	mu       sync.Mutex
	discover modelDiscovery
	cache    map[config.Harness]cachedModels
}

func newModelLookup(discover modelDiscovery) *modelLookup {
	return &modelLookup{discover: discover, cache: make(map[config.Harness]cachedModels)}
}
func (l *modelLookup) get(ctx context.Context, selected config.Harness) cachedModels {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Discovery depends on the endpoint/login, not the model being chosen.
	key := selected
	key.Model, key.Effort, key.MaxTokens = "", "", 0
	entry, found := l.cache[key]
	if found && time.Now().Before(entry.expires) {
		return entry
	}
	models, err := l.discover(ctx, selected.Provider())
	entry = cachedModels{value: listed(models), expires: time.Now().Add(time.Minute)}
	if err != nil {
		entry.detail = "Could not list the endpoint's models. Check its address and key, then retry. Your saved selection is unchanged."
		if harness.Engine(selected.Engine).Transport() == harness.CLITransport {
			entry.detail = "Could not discover models. Check the selected CLI login and installation, then retry. Your saved selection is unchanged."
		}
		entry.expires = time.Now().Add(5 * time.Second)
	}
	if len(l.cache) >= 32 {
		clear(l.cache)
	}
	l.cache[key] = entry
	return entry
}
func modelHandlerWithLookup(a *app.App, lookup *modelLookup) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile := r.URL.Query().Get("profile")
		if profile == "" {
			profile = "assistant"
		}
		if profile != "assistant" {
			fail(w, http.StatusBadRequest, "unknown model profile")
			return
		}
		cfg := a.Config()
		// The cache is keyed by the CLI and login too, so changing either
		// discovers again.
		selected, defaults := cfg.AssistantHarness(), config.DefaultProfile().Model
		result := modelCatalog{Profile: profile, Engine: selected.Engine, Models: []listedModel{}, Current: modelSelection{selected.Model, selected.Effort}, Default: modelSelection{defaults.Model, defaults.Effort}}
		if a.Demo {
			result.Detail = "Model discovery is unavailable in the demo."
			respond(w, 200, result)
			return
		}
		if preview := r.URL.Query().Get("engine"); preview != "" {
			if !config.Supports(preview, config.UseModels) {
				fail(w, http.StatusBadRequest, "unknown model engine")
				return
			}
			selected = cfg.Harness(preview, selected.Model, selected.Effort)
			result.Engine = preview
		}
		if provider := r.URL.Query().Get("provider"); provider != "" {
			if _, ok := cfg.Engines.APIProvider(provider); !ok || selected.Engine != string(harness.OpenAICompatible) {
				fail(w, http.StatusBadRequest, "unknown API provider")
				return
			}
			selected = cfg.HarnessOn(selected.Engine, provider, selected.Model, selected.Effort)
		}
		if selected.APIProvider != "" {
			result.Provider = selected.APIProvider
		}
		if !config.Supports(selected.Engine, config.UseModels) {
			result.Detail = "Use advanced settings for this provider's model. The saved selection is unchanged."
			respond(w, 200, result)
			return
		}
		cli := harness.Engine(selected.Engine).Transport() == harness.CLITransport
		entry := lookup.get(r.Context(), selected)
		result.Available = entry.detail == ""
		result.Detail = entry.detail
		for _, m := range entry.value {
			result.Models = append(result.Models, listedModel{Model: m, Free: strings.HasSuffix(m.ID, ":free")})
		}
		if result.Available {
			result.Detail = "Models and reasoning options reported by your selected CLI installation."
			if !cli {
				result.Detail = "Models your endpoint lists."
			}
		}
		respond(w, 200, result)
	})
}

// listed keeps every model's efforts a list, empty when they aren't known.
func listed(models []catalog.Model) []catalog.Model {
	for i := range models {
		if models[i].Efforts == nil {
			models[i].Efforts = []catalog.Effort{}
		}
	}
	return models
}
