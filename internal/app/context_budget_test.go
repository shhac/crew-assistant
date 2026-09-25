package app

import (
	"context"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/lib-agent-harness/completion"
)

func TestATurnIsSizedToTheWindowItsModelStated(t *testing.T) {
	if contextBudget(0, 4096) != 0 {
		t.Fatal("an unstated window should leave the engine's default")
	}
	if got, want := contextBudget(200000, 4096), (200000-4096-8192)*3; got != want {
		t.Fatalf("budget %d, want %d", got, want)
	}
	if contextBudget(10000, 4096) != 8192*3 {
		t.Fatal("a tiny window should still leave room to say something")
	}
	unsized := engine.Config{MaxOutputTokens: 4096}
	if smallerBudget(unsized, 200000) != 0 {
		t.Fatal("a window larger than the default is not smaller than the request was")
	}
	if smallerBudget(unsized, 30000) != contextBudget(30000, 4096) {
		t.Fatal("a window smaller than the default should size the request")
	}
	a := testApp(t)
	ctx := context.Background()
	a.recordWindow(ctx, engine.Config{Engine: "claude", Model: "opus"}, engine.Usage{ContextWindow: 200000})
	a.recordWindow(ctx, engine.Config{Engine: "claude", Model: "opus"}, engine.Usage{})
	snap, _ := a.Core.Snapshot(ctx)
	if snap.ModelWindow("claude", "opus") != 200000 {
		t.Fatalf("windows %v", snap.ModelWindows)
	}
}

func TestATurnTooLongForANewlyChosenModelIsSizedToItAndTriedOnce(t *testing.T) {
	a := testApp(t)
	refused := &completion.RequestError{Kind: completion.ErrorContextLimit, Phase: completion.PhaseResponse}
	run := func(replies ...engine.Result) (engine.Result, error, []int) {
		var budgets []int
		a.chatInvoker = func(_ context.Context, cfg engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
			budgets = append(budgets, cfg.MaxContextBytes)
			reply := replies[min(len(budgets), len(replies))-1]
			if reply.Message == "" {
				return reply, refused
			}
			return reply, nil
		}
		result, err := a.runChatTurn(context.Background(), core.ChatTurn{ID: "t", Message: "Hello"})
		return result, err, budgets
	}
	small := engine.Usage{ContextWindow: 30000}
	if result, err, budgets := run(engine.Result{Usage: small}, engine.Result{Message: "Fits now"}); err != nil || result.Message != "Fits now" || len(budgets) != 2 || budgets[1] != contextBudget(30000, a.Config().Model.MaxTokens) {
		t.Fatalf("result %+v, %v, budgets %v", result, err, budgets)
	}
	for name, first := range map[string]engine.Result{
		"a refusal stating nothing":                      {},
		"a refusal stating a larger window":              {Usage: engine.Usage{ContextWindow: 1000000}},
		"a refusal after the turn already did something": {Usage: small, Actions: []engine.Action{{Name: "queue_task", Success: true}}},
	} {
		if _, err, budgets := run(first, engine.Result{Message: "Again"}); err == nil || len(budgets) != 1 {
			t.Errorf("%s was tried %d times: %v", name, len(budgets), err)
		}
	}
	if _, err, budgets := run(engine.Result{Message: "Fine", Usage: small}); err != nil || len(budgets) != 1 {
		t.Fatalf("a reply that fitted was tried %d times: %v", len(budgets), err)
	}
}
