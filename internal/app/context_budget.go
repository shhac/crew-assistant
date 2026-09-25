package app

import (
	"context"
	"errors"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/completion"
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
	_ = a.Core.RecordModelWindow(context.WithoutCancel(ctx), ec.Engine, ec.Model, usage.ContextWindow)
}

// providerContextLimit reports a request the model refused as too long, as
// opposed to one the daemon never sent.
func providerContextLimit(err error) bool {
	var failure *completion.RequestError
	return errors.As(err, &failure) && failure.Kind == completion.ErrorContextLimit && failure.Phase != completion.PhasePreflight
}
