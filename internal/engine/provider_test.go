package engine

import (
	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
)

// api is an OpenAI-compatible endpoint on this machine that takes no key.
func api(baseURL string) harness.Provider { return apiWithKey(baseURL, "") }

// apiWithKey is an endpoint whose key is read from env at each request.
func apiWithKey(baseURL, env string) harness.Provider {
	return config.Harness{Engine: string(harness.OpenAICompatible), BaseURL: baseURL, APIKeyEnv: env}.Provider()
}
