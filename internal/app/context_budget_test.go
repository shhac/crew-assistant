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
	a := testApp(t)
	ctx := context.Background()
	ec := engine.Config{Engine: "claude", Model: "opus", MaxOutputTokens: 4096}
	if !a.learnWindow(ctx, ec, engine.Usage{ContextWindow: 200000}) {
		t.Fatal("a first stated window should size the next request")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.ModelWindow("claude", "opus") != 200000 {
		t.Fatalf("windows %v", snap.ModelWindows)
	}
	ec.MaxContextBytes = contextBudget(200000, 4096)
	if a.learnWindow(ctx, ec, engine.Usage{ContextWindow: 200000}) || a.learnWindow(ctx, ec, engine.Usage{}) {
		t.Fatal("an unchanged or unstated window reported a smaller budget")
	}
}

func TestATurnTooLongForANewlyChosenModelIsSizedToItAndTriedOnce(t *testing.T) {
	a := testApp(t)
	var budgets []int
	refused := &completion.RequestError{Kind: completion.ErrorContextLimit, Phase: completion.PhaseResponse}
	a.chatInvoker = func(_ context.Context, cfg engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		budgets = append(budgets, cfg.MaxContextBytes)
		if len(budgets) == 1 {
			return engine.Result{Usage: engine.Usage{ContextWindow: 30000}}, refused
		}
		return engine.Result{Message: "Fits now"}, nil
	}
	result, err := a.runChatTurn(context.Background(), core.ChatTurn{ID: "t", Message: "Hello"})
	if err != nil || result.Message != "Fits now" || len(budgets) != 2 || budgets[1] != contextBudget(30000, a.Config().Model.MaxTokens) {
		t.Fatalf("result %+v, %v, budgets %v", result, err, budgets)
	}

	// A refusal that states nothing new isn't tried again.
	budgets = nil
	a.chatInvoker = func(_ context.Context, cfg engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		budgets = append(budgets, cfg.MaxContextBytes)
		return engine.Result{}, refused
	}
	if _, err := a.runChatTurn(context.Background(), core.ChatTurn{ID: "u", Message: "Hello"}); err == nil || len(budgets) != 1 {
		t.Fatalf("tried %d times: %v", len(budgets), err)
	}
}
