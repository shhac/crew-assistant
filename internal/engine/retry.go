package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/lib-agent-harness/completion"
)

// RetryPolicy applies within one completion request, never to a conversation or
// its tool effects. MaxElapsed starts at the first explicit provider rejection;
// a healthy initial invocation keeps the engine's normal timeout.
type RetryPolicy struct {
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	MaxElapsed   time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 3, InitialDelay: time.Second, MaxDelay: 15 * time.Second, MaxElapsed: 2 * time.Minute}
}
func normalizedRetryPolicy(in *RetryPolicy) (RetryPolicy, error) {
	p := DefaultRetryPolicy()
	if in != nil {
		p.MaxRetries = in.MaxRetries
		if in.InitialDelay != 0 {
			p.InitialDelay = in.InitialDelay
		}
		if in.MaxDelay != 0 {
			p.MaxDelay = in.MaxDelay
		}
		if in.MaxElapsed != 0 {
			p.MaxElapsed = in.MaxElapsed
		}
	}
	if p.MaxRetries < 0 || p.MaxRetries > 10 || p.InitialDelay <= 0 || p.MaxDelay < p.InitialDelay || p.MaxDelay > 30*time.Minute || p.MaxElapsed <= 0 || p.MaxElapsed > 30*time.Minute {
		return p, errors.New("invalid model retry limits")
	}
	return p, nil
}

type RetryEvent struct {
	Status     string
	Attempt    int
	MaxRetries int
	Kind       completion.ErrorKind
	Delay      time.Duration
	RetryAt    time.Time
}

type requestAdmissionError struct{ err error }

func (e *requestAdmissionError) Error() string { return e.err.Error() }

// Preserve cancellation/budget identity without exposing a provider classification
// through errors.As to a caller with its own durable retry scheduler.
func (e *requestAdmissionError) Is(target error) bool { return errors.Is(e.err, target) }
func guardedAdmission(fn func(context.Context) error) func(context.Context) error {
	if fn == nil {
		return nil
	}
	return func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			return &requestAdmissionError{err}
		}
		return nil
	}
}

// The retry boundary ends before any coordination tool can execute. All attempts
// receive identical dialogue/tools, and each provider call retains its admission
// check. Rejected requests without token accounting keep aggregate usage unknown.
func (e *Engine) completeWithTools(ctx context.Context, messages []Message, tools []Tool) (Message, Usage, error) {
	var usage Usage
	policy := *e.cfg.Retry
	callCtx := ctx
	var recoveryDeadline time.Time
	var lastKind completion.ErrorKind
	for attempt := 0; ; attempt++ {
		if err := callCtx.Err(); err != nil {
			return Message{}, usage, err
		}
		m, u, err := e.completeAttemptWithTools(callCtx, messages, tools)
		mergeContextUsage(&usage, u, attempt == 0)
		if err == nil {
			if attempt > 0 {
				if hookErr := e.retryEvent(ctx, RetryEvent{Status: "recovered", Attempt: attempt, MaxRetries: policy.MaxRetries, Kind: lastKind}); hookErr != nil {
					return Message{}, usage, hookErr
				}
			}
			return m, usage, nil
		}
		var admission *requestAdmissionError
		var rejection *completion.RequestError
		if errors.As(err, &admission) || !errors.As(err, &rejection) || !rejection.Retryable() {
			return Message{}, usage, err
		}
		if callCtx.Err() != nil {
			return Message{}, usage, callCtx.Err()
		}
		lastKind = rejection.Kind
		if attempt >= policy.MaxRetries {
			if hookErr := e.retryEvent(ctx, RetryEvent{Status: "exhausted", Attempt: attempt, MaxRetries: policy.MaxRetries, Kind: rejection.Kind}); hookErr != nil {
				return Message{}, usage, hookErr
			}
			return Message{}, usage, fmt.Errorf("model retry allowance exhausted after %d retries: %w", attempt, err)
		}
		now := e.retryNow()
		if recoveryDeadline.IsZero() {
			recoveryDeadline = now.Add(policy.MaxElapsed)
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, policy.MaxElapsed)
			defer cancel()
		}
		delay := e.retryDelay(attempt, rejection.RetryAfter)
		remaining := recoveryDeadline.Sub(now)
		if parentDeadline, ok := ctx.Deadline(); ok && time.Until(parentDeadline) < remaining {
			remaining = time.Until(parentDeadline)
		}
		// Never shorten a provider's Retry-After to squeeze an early call into budget.
		if remaining <= 0 || delay >= remaining {
			if hookErr := e.retryEvent(ctx, RetryEvent{Status: "exhausted", Attempt: attempt, MaxRetries: policy.MaxRetries, Kind: rejection.Kind}); hookErr != nil {
				return Message{}, usage, hookErr
			}
			return Message{}, usage, fmt.Errorf("model recovery deadline prevents another attempt: %w", err)
		}
		event := RetryEvent{Status: "waiting", Attempt: attempt + 1, MaxRetries: policy.MaxRetries, Kind: rejection.Kind, Delay: delay, RetryAt: now.Add(delay)}
		if hookErr := e.retryEvent(callCtx, event); hookErr != nil {
			return Message{}, usage, hookErr
		}
		if waitErr := e.retrySleep(callCtx, delay); waitErr != nil {
			return Message{}, usage, waitErr
		}
		if callCtx.Err() != nil {
			return Message{}, usage, callCtx.Err()
		}
		event.Status = "retrying"
		event.Delay = 0
		event.RetryAt = time.Time{}
		if hookErr := e.retryEvent(callCtx, event); hookErr != nil {
			return Message{}, usage, hookErr
		}
	}
}
func (e *Engine) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	p := *e.cfg.Retry
	backoff := p.InitialDelay
	for i := 0; i < attempt && backoff < p.MaxDelay; i++ {
		if backoff > p.MaxDelay/2 {
			backoff = p.MaxDelay
		} else {
			backoff *= 2
		}
	}
	if backoff > p.MaxDelay {
		backoff = p.MaxDelay
	}
	random := e.retryRandom()
	if random < 0 {
		random = 0
	}
	if random > 1 {
		random = 1
	}
	// Equal jitter retains a real wait while spreading concurrent clients.
	delay := time.Duration(float64(backoff) * (0.5 + 0.5*random))
	if delay <= 0 {
		delay = time.Nanosecond
	}
	if retryAfter > delay {
		delay = retryAfter
	}
	return delay
}
func (e *Engine) retryEvent(ctx context.Context, event RetryEvent) error {
	if e.cfg.OnRetry != nil {
		return e.cfg.OnRetry(ctx, event)
	}
	return nil
}
func sleepForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
