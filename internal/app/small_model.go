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
	"github.com/shhac/lib-agent-harness/completion"
)

type smallCompletion func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error)
type smallDiscovery func(context.Context, engine.Config) ([]engine.ModelOption, error)

// smallModels answers loading captions and next-message suggestions with the
// approved small models: the assistant's own CLI first, then the other one.
// Each attempt is bounded and never retried, and an engine that just failed
// rests for a while, so an uninstalled, signed-out or exhausted CLI costs
// nothing on the next message. An engine whose login last reported nothing
// left isn't tried either, as the sidebar shows it.
type smallModels struct {
	discover smallDiscovery
	complete smallCompletion
	// outOfUsage says the login last reported nothing left, without reading
	// it again; nil knows of no reading.
	outOfUsage func(config.Harness) bool
	workDir    func() string // Resolved per call; the service may be absent.
	attempt    time.Duration // Bound on one engine's discovery and reply.
	rest       time.Duration // How long a failed engine is skipped.
	now        func() time.Time
	mu         sync.Mutex
	resting    map[string]restingEngine
}

// restingEngine keeps why an engine failed, so a skip during its rest reports
// the same cause: a model the login does not offer stays a mapping problem the
// owner is told about, whichever feature found it.
type restingEngine struct {
	until time.Time
	cause error
}

func newSmallModels(workDir func() string) *smallModels {
	return &smallModels{discover: engine.DiscoverModels, complete: engine.Complete, workDir: workDir, attempt: 8 * time.Second, rest: 10 * time.Minute, now: time.Now, resting: map[string]restingEngine{}}
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
	failure := &smallModelFailure{}
	for _, m := range models {
		// Only the approved pair is ever sent, whatever the caller passed.
		if !config.ApprovedSmallModel(m.Engine, m.Model) {
			failure.attempts = append(failure.attempts, fmt.Errorf("%s on %s is not an approved small model", m.Model, m.Engine))
			continue
		}
		if cause := s.restingCause(m.Engine); cause != nil {
			failure.attempts = append(failure.attempts, fmt.Errorf("the %s CLI is skipped after a recent failure: %w", m.Engine, cause))
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
			s.setResting(m.Engine, restingEngine{})
			return reply, nil
		}
		s.setResting(m.Engine, restingEngine{until: s.now().Add(s.rest), cause: err})
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
	models, err := s.discover(ctx, ec)
	if err != nil {
		return engine.Config{}, err
	}
	for _, option := range models {
		if option.ID != m.Model {
			continue
		}
		// An approved model without effort levels (Haiku 4.5) gets none.
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
	var refused *completion.RequestError
	if s.now().Before(r.until) && errors.As(r.cause, &refused) && refused.Kind == completion.ErrorRateLimited {
		return r.until
	}
	return time.Time{}
}

func (s *smallModels) setResting(engineName string, r restingEngine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resting[engineName] = r
}
