package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
)

func TestEnginePauseOwnerChatStillReplies(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	engineName := a.Config().AssistantHarness().Engine
	if err := a.Work.PauseEngine(ctx, engineName, nil); err != nil {
		t.Fatal(err)
	}
	called := false
	a.chatInvoker = func(context.Context, engine.Config, engine.Request, engine.ToolExecutor) (engine.Result, error) {
		called = true
		return engine.Result{Message: "Still here"}, nil
	}
	startTestQueue(t, a)
	if _, err := a.EnqueueChat(ctx, "owner-paused", "Hello"); err != nil {
		t.Fatal(err)
	}
	waitTurn(t, a, "owner-paused", "completed")
	if !called {
		t.Fatal("owner chat held")
	}
	snap, err := a.Snapshot(ctx)
	if err != nil || snap.Assistant.Engine != engineName {
		t.Fatal(snap.Assistant, err)
	}
	if _, paused := snap.EnginePaused(engineName, time.Now()); !paused {
		t.Fatal("chat resumed engine")
	}
}

func TestEnginePauseSlackProjectIntakeQueuesPlainly(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	ctx := context.Background()
	if err := a.Work.PauseEngine(ctx, "claude", nil); err != nil {
		t.Fatal(err)
	}
	event := slackapi.Message{ID: "engine-paused", Channel: "D_SYNTHETIC", Text: "What next?"}
	response := <-slackAsync(t, a, event)
	if response.err != nil || !strings.Contains(response.text, "Claude is paused until you resume") {
		t.Fatal(response)
	}
	messages, err := a.Core.PMChat(ctx, p.ID)
	if err != nil || len(messages) != 1 || messages[0].Status != "waiting" {
		t.Fatal(messages, err)
	}
}

func TestEnginePauseSlackUsesTheWorkClock(t *testing.T) {
	a := testApp(t)
	project := slackProject(t, a, true)
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	if err := a.Work.PauseEngine(ctx, "claude", &until); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Work.SendPMChat(ctx, project.ID, "expired-slack", "What next?"); err != nil {
		t.Fatal(err)
	}
	// Past the pause end, Slack waits for the PM reply instead of returning
	// a stale pause notice. Cancellation makes that distinction deterministic.
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Work.Now = func() time.Time { cancel(); return until }
	_, err := a.waitSlackPM(cancelCtx, slackapi.Message{}, project.ID, "expired-slack")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
