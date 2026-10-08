package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/engine"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/catalog"
)

type smallCompletion func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error)
type smallDiscovery func(context.Context, harness.Provider) ([]catalog.Model, error)

// smallModels answers loading captions and next-message suggestions with the
// models config.SmallModels lists: the model the owner chose in Settings, or
// else the approved small models, the assistant's own CLI first, then the
// other one. Each attempt is bounded and never retried, and an engine that just failed
// rests for a while, so an uninstalled, signed-out or exhausted CLI costs
// nothing on the next message. An engine whose login last reported nothing
// left isn't tried either, as the sidebar shows it.
type smallModels struct {
	discover smallDiscovery
	complete smallCompletion
	// outOfUsage says the login last reported nothing left, without reading
	// it again; nil knows of no reading.
	outOfUsage func(config.Harness) bool
	// recheck reads a login's usage again after its model refused a
	// request; nil reads nothing.
	recheck func(config.Harness)
	// busy marks a request in flight until the func it returns is called,
	// so team roles start no new turn meanwhile; nil marks nothing.
	busy    func() func()
	workDir func() string // Resolved per call; the service may be absent.
	attempt time.Duration // Bound on one engine's discovery and reply.
	rest    time.Duration // How long a failed engine is skipped.
	now     func() time.Time
	mu      sync.Mutex
	resting map[string]restingEngine
}

// restingEngine keeps why an engine failed, so a skip during its rest reports
// the same cause: a model the login does not offer stays a mapping problem the
// owner is told about, whichever feature found it.
type restingEngine struct {
	until time.Time
	cause error
}

func newSmallModels(workDir func() string) *smallModels {
	return &smallModels{discover: catalog.Discover, complete: engine.Complete, workDir: workDir, attempt: 8 * time.Second, rest: 10 * time.Minute, now: time.Now, resting: map[string]restingEngine{}}
}

// notOfferedError means the approved model, at its approved effort, is not
// available to that CLI's login.
type notOfferedError struct{ engine, model, effort string }

func (e *notOfferedError) Error() string {
	if e.effort != "" {
		return fmt.Sprintf("%s on the %s login does not offer %s effort", e.model, e.engine, e.effort)
	}
	return fmt.Sprintf("%s is not offered to the %s login", e.model, e.engine)
}

// smallModelFailure records why each approved model gave no reply.
type smallModelFailure struct{ attempts []error }

func (f *smallModelFailure) Error() string {
	parts := make([]string, len(f.attempts))
	for i, err := range f.attempts {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "; ")
}
func (f *smallModelFailure) Unwrap() []error { return f.attempts }

// notOffered reports that every approved model was refused by its login: a
// mapping problem for the owner, not a passing outage.
func (f *smallModelFailure) notOffered() bool {
	for _, err := range f.attempts {
		var refused *notOfferedError
		if !errors.As(err, &refused) {
			return false
		}
	}
	return len(f.attempts) > 0
}

// ask returns the first reply from the approved models in order. No tools are
// offered. The reply is the caller's to validate.
func (s *smallModels) ask(ctx context.Context, models []config.Harness, prompt []engine.Message, reserve func(context.Context) error) (engine.Message, error) {
	if s.busy != nil {
		defer s.busy()()
	}
	failure := &smallModelFailure{}
	for _, m := range models {
		// Only an engine that can do small jobs is ever reached, whatever
		// the caller passed.
		if !config.Supports(m.Engine, config.UseSmall) {
			failure.attempts = append(failure.attempts, fmt.Errorf("%s can't write suggestions", m.Engine))
			continue
		}
		if cause := s.restingCause(restKey(m)); cause != nil {
			failure.attempts = append(failure.attempts, fmt.Errorf("%s is skipped after a recent failure: %w", restName(m), cause))
			continue
		}
		if s.outOfUsage != nil && s.outOfUsage(m) {
			failure.attempts = append(failure.attempts, fmt.Errorf("the %s CLI is skipped: its login reports no usage left", m.Engine))
			continue
		}
		reply, err := s.try(ctx, m, prompt, reserve)
		if ctx.Err() != nil {
			// The caller stopped waiting; that says nothing about the engine.
			return engine.Message{}, ctx.Err()
		}
		if err == nil {
			s.setResting(restKey(m), restingEngine{})
			return reply, nil
		}
		s.rested(m, err)
		if s.recheck != nil {
			s.recheck(m)
		}
		failure.attempts = append(failure.attempts, err)
	}
	return engine.Message{}, failure
}

func (s *smallModels) try(ctx context.Context, m config.Harness, prompt []engine.Message, reserve func(context.Context) error) (engine.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, s.attempt)
	defer cancel()
	ec, err := s.verify(ctx, m, reserve)
	if err != nil {
		return engine.Message{}, err
	}
	reply, _, err := s.complete(ctx, ec, prompt, []engine.Tool{})
	return reply, err
}

// verify confirms the exact model is offered to the CLI's login before any
// inference. It never substitutes another model or raises the effort: a model
// with effort levels but not the approved one is treated as not offered.
func (s *smallModels) verify(ctx context.Context, m config.Harness, reserve func(context.Context) error) (engine.Config, error) {
	ec := EngineConfig(m)
	ec.Effort, ec.WorkDirRoot, ec.MaxOutputTokens, ec.MaxContextBytes, ec.Timeout, ec.Retry, ec.BeforeRequest = "", s.workDir(), 128, 8192, s.attempt, &engine.RetryPolicy{MaxRetries: 0}, reserve
	// An engine that lists no efforts, such as an API, has nothing to check
	// against; the owner named the model, and the endpoint refuses one it
	// doesn't serve.
	if !config.Supports(m.Engine, config.UseEfforts) {
		ec.Effort = m.Effort
		return ec, nil
	}
	models, err := s.discover(ctx, ec.Provider)
	if err != nil {
		return engine.Config{}, err
	}
	for _, option := range models {
		if option.ID != m.Model {
			continue
		}
		if !option.EffortsKnown {
			ec.Effort = m.Effort
			return ec, nil
		}
		// An approved model without effort levels (Haiku before 5.5) gets none.
		for _, effort := range option.Efforts {
			if effort.ID == m.Effort {
				ec.Effort = m.Effort
			}
		}
		if m.Effort != "" && len(option.Efforts) > 0 && ec.Effort == "" {
			return engine.Config{}, &notOfferedError{engine: m.Engine, model: m.Model, effort: m.Effort}
		}
		return ec, nil
	}
	return engine.Config{}, &notOfferedError{engine: m.Engine, model: m.Model}
}

// restKey is what a failure rests: a CLI engine, or one API route, so a free
// model's rate limit on one provider or account leaves the others to try.
func restKey(m config.Harness) string {
	if m.APIProvider == "" {
		return m.Engine
	}
	return m.Engine + "/" + apiRoute(m.APIProvider, m.BaseURL, m.APIKeyEnv)
}

// restName is how a skip names what rests.
func restName(m config.Harness) string {
	if m.APIProvider == "" {
		return fmt.Sprintf("the %s CLI", m.Engine)
	}
	return fmt.Sprintf("the %s API provider", m.APIProvider)
}

// rested rests what m runs on after err: for the usual while, or as long as
// a rate limit's Retry-After asks if that is longer.
func (s *smallModels) rested(m config.Harness, err error) {
	rest := s.rest
	if facts, ok := harness.ErrorFacts(err); ok && facts.Cause == harness.CauseRateLimited {
		rest = max(rest, facts.RetryAfter)
	}
	s.setResting(restKey(m), restingEngine{until: s.now().Add(rest), cause: err})
}

// restingCause is why the engine last failed while it is still resting, or nil
// when it may be tried.
func (s *smallModels) restingCause(engineName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.resting[engineName]; s.now().Before(r.until) {
		return r.cause
	}
	return nil
}

// rateLimitedUntil is when an engine that refused for its rate limit is tried
// again; zero when it isn't resting for that.
func (s *smallModels) rateLimitedUntil(engineName string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.resting[engineName]
	if facts, ok := harness.ErrorFacts(r.cause); ok && s.now().Before(r.until) && facts.Cause == harness.CauseRateLimited {
		return r.until
	}
	return time.Time{}
}

func (s *smallModels) setResting(engineName string, r restingEngine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resting[engineName] = r
}
