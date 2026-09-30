package app

import (
	"context"

	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
)

// contextBudget is how many bytes a request to a model may carry: its stated
// window, less room for the reply and the CLI's own framing, at three bytes a
// token, which undercounts prose and JSON alike. Until the model has stated a
// window, the engine's default stands.
func contextBudget(window, maxOutput int) int {
	if window <= 0 {
		return 0
	}
	return max(window-maxOutput-8192, 8192) * 3
}

// smallerBudget is the budget a stated window gives, when it is smaller than
// the request was sized for; otherwise 0.
func smallerBudget(ec engine.Config, window int) int {
	sized := ec.MaxContextBytes
	if sized == 0 {
		sized = engine.DefaultContextBytes
	}
	budget := contextBudget(window, ec.MaxOutputTokens)
	if budget == 0 || budget >= sized {
		return 0
	}
	return budget
}

// recordWindow keeps the window a reply stated for the model that gave it.
// A window not kept is learned again from the next reply.
func (a *App) recordWindow(ctx context.Context, ec engine.Config, usage engine.Usage) {
	_ = a.Core.RecordModelWindow(context.WithoutCancel(ctx), modelHome(ec), ec.Model, usage.ContextWindow)
}

// modelHome names where ec's model runs, for what is kept about it: its
// engine, and on an API its route too, since two providers can serve
// different models under the same id, even at one address.
func modelHome(ec engine.Config) string {
	if endpoint := modelEndpoint(ec); endpoint != "" {
		return ec.Engine() + ":" + apiRoute(ec.APIProvider, endpoint, ec.APIKeyEnv)
	}
	return ec.Engine()
}

// apiRoute names how an API model is reached, and with whose account: the
// named provider, its address and the variable its key is read from, never
// the key. A provider edited in place to another address or key is another
// route, with its own windows, sessions and rate limits.
func apiRoute(provider, endpoint, keyEnv string) string {
	return provider + "@" + endpoint + "#" + keyEnv
}

// modelEndpoint is the address an API model is reached at; empty for a CLI.
func modelEndpoint(ec engine.Config) string {
	if ec.Provider.Engine.Transport() != harness.APITransport {
		return ""
	}
	return ec.Provider.API.BaseURL
}

// providerContextLimit reports a request the model refused as too long, as
// opposed to one the daemon never sent.
func providerContextLimit(err error) bool {
	facts, ok := harness.ErrorFacts(err)
	return ok && facts.Cause == harness.CauseContextLimit && facts.Family != harness.FailurePreflight && facts.Family != harness.FailureCapability
}
