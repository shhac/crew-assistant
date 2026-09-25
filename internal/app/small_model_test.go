package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
)

// fakeCLIs stands in for both CLI logins. Each offers its approved model among
// others, so a test can see that nothing else is ever asked for.
type fakeCLIs struct {
	t           *testing.T
	mu          sync.Mutex
	offered     map[string][]engine.ModelOption
	discoverErr map[string]error
	replyErr    map[string]error
	block       bool // Replies wait for their deadline.
	reply       string
	discovered  []string
	completed   []engine.Config
	clock       time.Time
}

func newFakeCLIs(t *testing.T) *fakeCLIs {
	return &fakeCLIs{
		t: t,
		offered: map[string][]engine.ModelOption{
			"codex":  {{ID: "gpt-6-astra", Efforts: []engine.ModelEffort{{ID: "high"}}}, {ID: "gpt-5.6-luna", Efforts: []engine.ModelEffort{{ID: "low"}}}, {ID: "gpt-6-luna", Efforts: []engine.ModelEffort{{ID: "low"}, {ID: "high"}}}},
			"claude": {{ID: "opus"}, {ID: "haiku"}},
		},
		discoverErr: map[string]error{},
		replyErr:    map[string]error{},
		reply:       "Planting the next idea",
		clock:       time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
	}
}

func (f *fakeCLIs) models() *smallModels {
	dir := f.t.TempDir()
	s := newSmallModels(func() string { return dir })
	s.now = func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.clock }
	s.discover = func(_ context.Context, c engine.Config) ([]engine.ModelOption, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.discovered = append(f.discovered, c.Engine)
		if err := f.discoverErr[c.Engine]; err != nil {
			return nil, err
		}
		return f.offered[c.Engine], nil
	}
	s.complete = func(ctx context.Context, c engine.Config, _ []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
		f.mu.Lock()
		f.completed = append(f.completed, c)
		err, block := f.replyErr[c.Engine], f.block
		f.mu.Unlock()
		if len(tools) != 0 || c.Retry == nil || c.Retry.MaxRetries != 0 {
			f.t.Errorf("small model given tools or retries: %v %+v", tools, c.Retry)
		}
		if block {
			<-ctx.Done()
			return engine.Message{}, engine.Usage{}, ctx.Err()
		}
		if err != nil {
			return engine.Message{}, engine.Usage{}, err
		}
		return engine.Message{Content: f.reply}, engine.Usage{}, nil
	}
	return s
}

func (f *fakeCLIs) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = f.clock.Add(d)
}

// used lists the engine/model/effort of every completion, and fails the test
// if anything but an approved small model was sent.
func (f *fakeCLIs) used() []string {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for _, c := range f.completed {
		if !config.ApprovedSmallModel(c.Engine, c.Model) {
			f.t.Fatalf("unapproved model used: %s/%s", c.Engine, c.Model)
		}
		out = append(out, c.Engine+"/"+c.Model+"/"+c.Effort)
	}
	return out
}

func (f *fakeCLIs) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.discovered) + len(f.completed)
}

func smallModelsFor(t *testing.T, engineName string) []config.Harness {
	t.Helper()
	cfg := config.Default()
	cfg.Model.Engine = engineName
	models, err := cfg.SmallModels()
	if err != nil {
		t.Fatal(err)
	}
	return models
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSmallModelsUseTheOwnEngineFirst(t *testing.T) {
	for engineName, want := range map[string]string{"codex": "codex/gpt-6-luna/low", "claude": "claude/haiku/"} {
		f := newFakeCLIs(t)
		if _, err := f.models().ask(context.Background(), smallModelsFor(t, engineName), nil, nil); err != nil {
			t.Fatal(engineName, err)
		}
		if got := f.used(); !equalStrings(got, []string{want}) {
			t.Fatal(engineName, got)
		}
	}
}

func TestSmallModelsFallBackFromCodexToClaude(t *testing.T) {
	for name, outage := range map[string]func(*fakeCLIs){
		"not installed or signed out": func(f *fakeCLIs) { f.discoverErr["codex"] = errors.New("codex: not logged in") },
		"luna not offered":            func(f *fakeCLIs) { f.offered["codex"] = f.offered["codex"][:2] },
		"low effort not offered":      func(f *fakeCLIs) { f.offered["codex"][2].Efforts = []engine.ModelEffort{{ID: "high"}} },
		"out of usage":                func(f *fakeCLIs) { f.replyErr["codex"] = errors.New("usage limit reached") },
		"rate limited":                func(f *fakeCLIs) { f.replyErr["codex"] = errors.New("429 rate limited") },
	} {
		f := newFakeCLIs(t)
		outage(f)
		reply, err := f.models().ask(context.Background(), smallModelsFor(t, "codex"), nil, nil)
		if err != nil || reply.Content == "" {
			t.Fatal(name, err)
		}
		// Haiku is used with no effort, and never Luna 5.6, Astra or Opus.
		used := f.used()
		if used[len(used)-1] != "claude/haiku/" {
			t.Fatal(name, used)
		}
	}
}

func TestSmallModelsFallBackFromClaudeToCodex(t *testing.T) {
	for name, outage := range map[string]func(*fakeCLIs){
		"not installed":     func(f *fakeCLIs) { f.discoverErr["claude"] = errors.New("claude: executable not found") },
		"haiku not offered": func(f *fakeCLIs) { f.offered["claude"] = f.offered["claude"][:1] },
		"call fails":        func(f *fakeCLIs) { f.replyErr["claude"] = errors.New("overloaded") },
	} {
		f := newFakeCLIs(t)
		outage(f)
		if _, err := f.models().ask(context.Background(), smallModelsFor(t, "claude"), nil, nil); err != nil {
			t.Fatal(name, err)
		}
		used := f.used()
		if used[len(used)-1] != "codex/gpt-6-luna/low" {
			t.Fatal(name, used)
		}
	}
}

func TestSmallModelsSkipAFailedEngineForAWhile(t *testing.T) {
	f := newFakeCLIs(t)
	f.replyErr["codex"] = errors.New("429 rate limited")
	s := f.models()
	models := smallModelsFor(t, "codex")
	if _, err := s.ask(context.Background(), models, nil, nil); err != nil {
		t.Fatal(err)
	}
	f.discovered, f.completed = nil, nil
	// The next message goes straight to Claude without touching Codex.
	if _, err := s.ask(context.Background(), models, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(f.discovered, []string{"claude"}) || !equalStrings(f.used(), []string{"claude/haiku/"}) {
		t.Fatal(f.discovered, f.used())
	}
	// Once the rest is over, the assistant's own engine is tried again.
	delete(f.replyErr, "codex")
	f.advance(s.rest + time.Second)
	f.discovered, f.completed = nil, nil
	if _, err := s.ask(context.Background(), models, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(f.used(), []string{"codex/gpt-6-luna/low"}) {
		t.Fatal(f.used())
	}
}

func TestSmallModelsBothFailingAreBoundedAndThenCostNothing(t *testing.T) {
	f := newFakeCLIs(t)
	f.block = true
	s := f.models()
	s.attempt = 20 * time.Millisecond
	started := time.Now()
	_, err := s.ask(context.Background(), smallModelsFor(t, "codex"), nil, nil)
	var failure *smallModelFailure
	if !errors.As(err, &failure) || failure.notOffered() || len(f.completed) != 2 {
		t.Fatal(err, len(f.completed))
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatal("hung engines were not bounded", elapsed)
	}
	// While both rest, a message costs no CLI process at all.
	before := f.calls()
	if _, err = s.ask(context.Background(), smallModelsFor(t, "codex"), nil, nil); !errors.As(err, &failure) || f.calls() != before {
		t.Fatal(err, f.calls()-before)
	}
}

func TestSmallModelsReportWhenNeitherLoginOffersItsApprovedModel(t *testing.T) {
	f := newFakeCLIs(t)
	f.offered["codex"] = f.offered["codex"][:2]
	f.offered["claude"] = f.offered["claude"][:1]
	s := f.models()
	_, err := s.ask(context.Background(), smallModelsFor(t, "codex"), nil, nil)
	var failure *smallModelFailure
	if !errors.As(err, &failure) || !failure.notOffered() || len(f.completed) != 0 {
		t.Fatal(err, f.completed)
	}
	// While both rest, the skip keeps its cause: still a mapping problem, found
	// without asking either CLI again.
	before := f.calls()
	_, err = s.ask(context.Background(), smallModelsFor(t, "claude"), nil, nil)
	if !errors.As(err, &failure) || !failure.notOffered() || f.calls() != before {
		t.Fatal(err, f.calls()-before)
	}
	// One outage alongside one refusal is a passing problem, not a mapping one.
	f = newFakeCLIs(t)
	f.offered["codex"] = f.offered["codex"][:2]
	f.replyErr["claude"] = errors.New("overloaded")
	if _, err = f.models().ask(context.Background(), smallModelsFor(t, "codex"), nil, nil); !errors.As(err, &failure) || failure.notOffered() {
		t.Fatal(err)
	}
}

func TestSmallModelsNeverSendAnUnapprovedModel(t *testing.T) {
	f := newFakeCLIs(t)
	s := f.models()
	cfg := config.Default()
	astra, opus := cfg.AssistantHarness(), cfg.Harness("claude", "opus", "")
	if _, err := s.ask(context.Background(), []config.Harness{astra, opus}, nil, nil); err == nil || f.calls() != 0 {
		t.Fatal(err, f.calls())
	}
}

func TestSmallModelsDoNotBlameAnEngineWhenTheCallerStops(t *testing.T) {
	f := newFakeCLIs(t)
	f.block = true
	s := f.models()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.ask(ctx, smallModelsFor(t, "codex"), nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if s.restingCause("codex") != nil || s.restingCause("claude") != nil || len(f.completed) != 1 {
		t.Fatal("caller cancellation rested an engine", len(f.completed))
	}
}
