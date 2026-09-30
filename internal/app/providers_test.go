package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/testutil"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/completion"
	"github.com/shhac/lib-agent-harness/session"
)

func withProviders(cfg *config.Config) {
	cfg.Engines.Providers = []config.Provider{
		{ID: "openrouter", Name: "OpenRouter", HTTPEngine: config.HTTPEngine{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", EffortParameter: "reasoning.effort"}},
		{ID: "local", Name: "Local model", HTTPEngine: config.HTTPEngine{BaseURL: "http://127.0.0.1:11434/v1"}},
		// Another account at OpenRouter's own address.
		{ID: "work", Name: "Work OpenRouter", HTTPEngine: config.HTTPEngine{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_WORK_KEY", EffortParameter: "reasoning.effort"}},
	}
}

// otherProviders are what the assistant moves to from OpenRouter in the
// tests below: one at another address, and one at the same address with
// another account's key.
var otherProviders = map[string]string{"local": "http://127.0.0.1:11434/v1", "work": "https://openrouter.ai/api/v1"}

// providerApp is a chat app with the providers above, its assistant on
// provider's model, and CLIs that are missing, so reading usage never
// reaches a real login.
func providerApp(t *testing.T, provider, model string) *App {
	t.Helper()
	a := testApp(t)
	cfg := a.Config()
	withProviders(&cfg)
	missing := filepath.Join(t.TempDir(), "missing")
	cfg.Engines.Codex.Bin, cfg.Engines.Claude.Bin, cfg.Engines.Grok.Bin = filepath.Join(missing, "codex"), filepath.Join(missing, "claude"), filepath.Join(missing, "grok")
	assistant := seated(&cfg)
	assistant.Model.Provider, assistant.Model.Model = provider, model
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return a
}

// A free model's rate limit rests only the provider it came from, for as
// long as it asked if that is longer than the usual rest; the others are
// still tried.
func TestARateLimitRestsOnlyThatProvider(t *testing.T) {
	cfg := config.Default()
	withProviders(&cfg)
	free := cfg.HarnessOn("openai-compatible", "openrouter", "deepseek/deepseek-r1:free", "")
	local := cfg.HarnessOn("openai-compatible", "local", "llama3.2", "")
	f := newFakeCLIs(t)
	s := f.models()
	limited := &completion.RequestError{Cause: harness.CauseRateLimited, RetryAfter: 45 * time.Minute, Engine: harness.OpenAICompatible}
	var reached []string
	s.complete = func(_ context.Context, c engine.Config, _ []engine.Message, _ []engine.Tool) (engine.Message, engine.Usage, error) {
		reached = append(reached, c.Provider.API.BaseURL)
		if c.Provider.API.BaseURL == free.BaseURL {
			return engine.Message{}, engine.Usage{}, limited
		}
		return engine.Message{Content: "Reply"}, engine.Usage{}, nil
	}
	if _, err := s.ask(context.Background(), []config.Harness{free}, nil, nil); !errors.Is(err, limited) {
		t.Fatal(err)
	}
	if until := s.rateLimitedUntil(restKey(free)); !until.Equal(f.clock.Add(45 * time.Minute)) {
		t.Fatalf("rested until %v", until)
	}
	if reply, err := s.ask(context.Background(), []config.Harness{local}, nil, nil); err != nil || reply.Content != "Reply" {
		t.Fatal("another provider was rested too", err)
	}
	f.advance(20 * time.Minute)
	if _, err := s.ask(context.Background(), []config.Harness{free}, nil, nil); err == nil || !strings.Contains(err.Error(), "the openrouter API provider is skipped") {
		t.Fatal("tried again before its Retry-After", err)
	}
	if len(reached) != 2 {
		t.Fatal("reached", reached)
	}
	// A shorter Retry-After still rests the usual while.
	limited.RetryAfter = time.Minute
	f.advance(30 * time.Minute)
	_, _ = s.ask(context.Background(), []config.Harness{free}, nil, nil)
	if until := s.rateLimitedUntil(restKey(free)); !until.Equal(f.clock.Add(s.rest)) {
		t.Fatalf("rested until %v", until)
	}
}

// When the assistant's free model stays rate-limited through its retries,
// the owner is told which provider and why, the provider rests like a
// usage limit, and the sidebar shows it.
func TestAnAssistantRateLimitedOnAProviderIsShown(t *testing.T) {
	a := providerApp(t, "openrouter", "deepseek/deepseek-r1:free")
	var reached string
	a.chatInvoker = func(_ context.Context, ec engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		reached = ec.Provider.API.BaseURL
		return engine.Result{}, &completion.RequestError{Cause: harness.CauseRateLimited, RetryAfter: time.Hour, Engine: harness.OpenAICompatible}
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "Hello")
	turn := waitTurn(t, a, "one", "failed")
	if reached != "https://openrouter.ai/api/v1" {
		t.Fatal("reached", reached)
	}
	if !strings.Contains(turn.Error, "OpenRouter is rate-limiting") || !strings.Contains(turn.Error, "Free models have tight limits") {
		t.Fatal(turn.Error)
	}
	var shown *EngineUsage
	for _, u := range a.Usage(context.Background()) {
		if u.Provider == "openrouter" {
			shown = &u
		}
		if u.Provider == "local" {
			t.Fatal("a provider that wasn't limited is shown", u)
		}
	}
	if shown == nil || shown.Engine != "openai-compatible" || shown.Label != "OpenRouter" || shown.RateLimitedUntil == nil || time.Until(*shown.RateLimitedUntil) < 50*time.Minute || shown.Windows == nil {
		t.Fatalf("%+v", shown)
	}
	if cause := a.small.restingCause(restKey(a.Config().AssistantHarness())); cause == nil {
		t.Fatal("suggestions would still try the limited provider")
	}
}

// An endpoint answering as OpenRouter does for a free model over its limit,
// a 429 with Retry-After, is shown and backed off like any usage limit. It
// needs loopback, so it is skipped inside a sandboxed check.
func TestAFreeModelsRateLimitFromItsEndpointIsBackedOff(t *testing.T) {
	var calls atomic.Int32
	remote := testutil.NewModelServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-min.","code":429,"metadata":{"headers":{"X-RateLimit-Limit":"20","X-RateLimit-Remaining":"0"}}}}`))
	}))
	defer remote.Close()
	a := providerApp(t, "local", "meta-llama/llama-4:free")
	cfg := a.Config()
	cfg.Engines.Providers[1].BaseURL = remote.URL + "/v1"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "Hello")
	turn := waitTurn(t, a, "one", "failed")
	if calls.Load() == 0 || !strings.Contains(turn.Error, "Local model is rate-limiting") {
		t.Fatal(calls.Load(), turn.Error)
	}
	var until *time.Time
	for _, u := range a.Usage(context.Background()) {
		if u.Provider == "local" {
			until = u.RateLimitedUntil
		}
	}
	if until == nil || time.Until(*until) < 9*time.Minute {
		t.Fatalf("not backed off: %v", until)
	}
}

// switchProvider moves the seated assistant to another provider, keeping its
// model and effort.
func switchProvider(t *testing.T, a *App, provider string) {
	t.Helper()
	cfg := a.Config()
	seated(&cfg).Model.Provider = provider
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// setKeyEnv edits OpenRouter's provider in place to read its key from
// another variable: another account, under the same id and address.
func setKeyEnv(t *testing.T, a *App, name string) {
	t.Helper()
	cfg := a.Config()
	cfg.Engines.Providers = append([]config.Provider(nil), cfg.Engines.Providers...)
	cfg.Engines.Providers[0].APIKeyEnv = name
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// A provider edited in place to read another account's key keeps its id and
// address, but is another account: its window, session and rate limit are
// its own, and going back to the first key finds the first account's again.
func TestEditingAProvidersKeyInPlaceIsAnotherAccount(t *testing.T) {
	a := providerApp(t, "openrouter", "shared-model")
	ctx := context.Background()
	o := &openings{resume: func(*session.Ref) (bool, string) { return true, "" }}
	a.sessions.open = o.open
	a.summarize = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		return engine.Message{Content: "Earlier: hello."}, engine.Usage{}, nil
	}
	budget := func() int { return a.assistantConfig(ctx, a.Config()).MaxContextBytes }
	resting := func() bool {
		for _, u := range a.Usage(ctx) {
			if u.Provider == "openrouter" {
				return u.RateLimitedUntil != nil
			}
		}
		return false
	}
	// The first account's session states its window, and it is rate-limited.
	runTurn(t, a, "Hello")
	learned := budget()
	cfg := a.Config()
	a.restRateLimited(cfg, a.assistantConfig(ctx, cfg), &completion.RequestError{Cause: harness.CauseRateLimited})
	if learned == 0 || !resting() {
		t.Fatalf("the first account's window %d, resting %v", learned, resting())
	}

	setKeyEnv(t, a, "OPENROUTER_OTHER_KEY")
	if got := budget(); got != 0 {
		t.Fatalf("the other account was sized to the first's window: %d", got)
	}
	if resting() || a.small.restingCause(restKey(a.Config().AssistantHarness())) != nil {
		t.Fatal("the other account kept the first's rate limit")
	}
	runTurn(t, a, "Next")
	if len(o.chats) != 2 || !o.chats[0].closed || len(o.chats[0].sent) != 1 || o.chats[1].spec.Config.APIKeyEnv != "OPENROUTER_OTHER_KEY" {
		t.Fatalf("the next turn stayed on the first account's session: %d sessions", len(o.chats))
	}
	if o.refs[1] != nil {
		t.Fatal("the first account's transcript was carried on with the other's key")
	}
	record, _, _ := a.Core.ChatSession(ctx)
	if record == nil || record.Provider != "openrouter" || record.KeyEnv != "OPENROUTER_OTHER_KEY" {
		t.Fatalf("record %+v", record)
	}

	setKeyEnv(t, a, "OPENROUTER_API_KEY")
	if budget() != learned || !resting() {
		t.Fatalf("the first account's window %d or rate limit %v was lost", budget(), resting())
	}
}

// Two providers can serve different models under one id, even at one
// address with different accounts, so the window one states never sizes a
// turn to the other.
func TestEachProviderKeepsItsOwnWindowForAModelID(t *testing.T) {
	for other := range otherProviders {
		t.Run(other, func(t *testing.T) {
			a := providerApp(t, "openrouter", "shared-model")
			ctx := context.Background()
			budget := func() int { return a.assistantConfig(ctx, a.Config()).MaxContextBytes }
			a.recordWindow(ctx, a.assistantConfig(ctx, a.Config()), engine.Usage{ContextWindow: 1000000})
			large := contextBudget(1000000, a.Config().AssistantHarness().MaxTokens)
			if budget() != large {
				t.Fatalf("OpenRouter's window wasn't used: %d", budget())
			}
			switchProvider(t, a, other)
			if got := budget(); got != 0 {
				t.Fatalf("%s was sized to OpenRouter's window: %d", other, got)
			}
			a.recordWindow(ctx, a.assistantConfig(ctx, a.Config()), engine.Usage{ContextWindow: 32000})
			if got := budget(); got != contextBudget(32000, a.Config().AssistantHarness().MaxTokens) {
				t.Fatalf("%s's own window wasn't used: %d", other, got)
			}
			switchProvider(t, a, "openrouter")
			if budget() != large {
				t.Fatalf("OpenRouter's window was overwritten by %s's: %d", other, budget())
			}
		})
	}
}

// Moving the assistant to another provider with the same model and effort
// mid-conversation, at another address or at the same one with another
// account, sends the next turn there, on a new session that doesn't carry
// on the old provider's transcript; the same provider still resumes its own
// after a restart.
func TestSwitchingProviderMidConversationReachesTheNewProvider(t *testing.T) {
	for other, address := range otherProviders {
		t.Run(other, func(t *testing.T) {
			a := providerApp(t, "openrouter", "shared-model")
			o := &openings{resume: func(*session.Ref) (bool, string) { return true, "" }}
			a.sessions.open = o.open
			a.summarize = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
				return engine.Message{Content: "Earlier: hello."}, engine.Usage{}, nil
			}
			reached := func(i int) string {
				c := o.chats[i].spec.Config
				return c.APIProvider + "@" + c.Provider.API.BaseURL
			}
			runTurn(t, a, "Hello")
			if len(o.chats) != 1 || reached(0) != "openrouter@https://openrouter.ai/api/v1" {
				t.Fatalf("first turn reached %d sessions", len(o.chats))
			}
			switchProvider(t, a, other)
			runTurn(t, a, "Next")
			if len(o.chats) != 2 || reached(1) != other+"@"+address || len(o.chats[0].sent) != 1 || !o.chats[0].closed {
				t.Fatalf("the next turn stayed on the old provider: %d sessions", len(o.chats))
			}
			if o.refs[1] != nil {
				t.Fatalf("OpenRouter's transcript was carried on at %s", other)
			}
			record, _, _ := a.Core.ChatSession(context.Background())
			if record == nil || record.Engine != "openai-compatible" || record.Provider != other || record.Endpoint != address {
				t.Fatalf("record %+v", record)
			}
			// After a restart on the same provider, its own conversation resumes.
			a.closeChat()
			runTurn(t, a, "Again")
			if len(o.chats) != 3 || o.refs[2] == nil || reached(2) != other+"@"+address {
				t.Fatalf("%s's own session wasn't resumed", other)
			}
		})
	}
}

// A rate limit rests the provider the turn's request went to, as the config
// the turn was run from names it, even when Settings move the assistant to
// another provider before the turn ends.
func TestARateLimitRestsTheProviderTheTurnReached(t *testing.T) {
	a := providerApp(t, "openrouter", "shared-model")
	var reached string
	a.chatInvoker = func(_ context.Context, ec engine.Config, _ engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		reached = ec.APIProvider
		// Settings change while the request is out.
		switchProvider(t, a, "work")
		return engine.Result{}, &completion.RequestError{Cause: harness.CauseRateLimited, RetryAfter: time.Hour, Engine: harness.OpenAICompatible}
	}
	startTestQueue(t, a)
	a.EnqueueChat(context.Background(), "one", "Hello")
	turn := waitTurn(t, a, "one", "failed")
	if reached != "openrouter" || !strings.HasPrefix(turn.Error, "OpenRouter is rate-limiting") {
		t.Fatal(reached, turn.Error)
	}
	resting := map[string]bool{}
	for _, u := range a.Usage(context.Background()) {
		resting[u.Provider] = u.RateLimitedUntil != nil
	}
	if !resting["openrouter"] || resting["work"] {
		t.Fatalf("resting %v", resting)
	}

	// The rest follows the request's own config, not the config now in
	// force, and a provider since removed is still named.
	cfg := a.Config()
	ran := EngineConfig(cfg.HarnessOn("openai-compatible", "local", "m", ""))
	limited := &completion.RequestError{Cause: harness.CauseRateLimited}
	if err := a.restRateLimited(cfg, ran, limited); !strings.HasPrefix(chatFailureReason(err), "Local model is rate-limiting") {
		t.Fatal(chatFailureReason(err))
	}
	if a.small.rateLimitedUntil(restKey(cfg.HarnessOn("openai-compatible", "local", "", ""))).IsZero() || !a.small.rateLimitedUntil(restKey(a.Config().AssistantHarness())).IsZero() {
		t.Fatal("the rest wasn't the request's provider's")
	}
	gone := config.Default()
	if err := a.restRateLimited(gone, ran, limited); !strings.HasPrefix(chatFailureReason(err), "local is rate-limiting") {
		t.Fatal(chatFailureReason(err))
	}
}

// Only an API provider's rate limit is rested from the chat; a CLI's shows
// in its usage, and another failure rests nothing.
func TestOnlyAnAPIRateLimitRestsFromTheChat(t *testing.T) {
	a := providerApp(t, "", "")
	cfg := a.Config()
	limited := &completion.RequestError{Cause: harness.CauseRateLimited}
	if err := a.restRateLimited(cfg, EngineConfig(cfg.Harness("claude", "opus", "")), limited); err != limited {
		t.Fatal("a CLI's rate limit was wrapped", err)
	}
	overloaded := &completion.RequestError{Cause: harness.CauseOverloaded}
	if err := a.restRateLimited(cfg, EngineConfig(cfg.Harness("openai-compatible", "m", "")), overloaded); err != overloaded {
		t.Fatal("an overload was wrapped", err)
	}
	if len(a.Usage(context.Background())) != 2 {
		t.Fatal("a provider was shown without a rate limit")
	}
	err := a.restRateLimited(cfg, EngineConfig(cfg.Harness("openai-compatible", "m", "")), limited)
	if reason := chatFailureReason(err); !strings.Contains(reason, "api.openai.com is rate-limiting") {
		t.Fatal(reason)
	}
}
