package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/sandbox"

	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// checkRuns owns checks for the entire turn, not an individual MCP call.
// Call cancellation stops waiting only; cleanup cancels and settles the run.
type checkRuns struct {
	mu           sync.Mutex
	lp           *Loop
	medium       gitMedium
	from, cache  string
	ctx          context.Context
	cancel       context.CancelFunc
	wait         time.Duration
	cleanupError func(error)
	current      *checkRun
	closed       bool
}
type checkRun struct {
	done      chan struct{}
	start     time.Time
	result    sandbox.CommandResult
	err       error
	delivered bool
}

func newCheckRuns(lp *Loop, m gitMedium, from string, env []string) *checkRuns {
	ctx, cancel := context.WithCancel(context.Background())
	r := &checkRuns{lp: lp, medium: m, from: from, ctx: ctx, cancel: cancel, wait: 45 * time.Second}
	for _, e := range env {
		if path, ok := strings.CutPrefix(e, "GOCACHE="); ok {
			r.cache = filepath.Dir(path)
		}
	}
	return r
}
func (r *checkRuns) call(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", errors.New("the check's turn has ended")
	}
	run := r.current
	if run == nil || run.delivered {
		run = &checkRun{done: make(chan struct{}), start: time.Now()}
		r.current = run
		go r.run(run)
	}
	r.mu.Unlock()
	timer := time.NewTimer(r.wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return fmt.Sprintf("Check still running for %ds; call run_check again to wait for the same run. It won't start another.", int(time.Since(run.start).Seconds())), nil
	case <-run.done:
		r.mu.Lock()
		run.delivered = true
		r.mu.Unlock()
		if run.err != nil {
			return "", fmt.Errorf("the check could not be run: %w", run.err)
		}
		return commandResultJSON(run.result)
	}
}
func (r *checkRuns) run(run *checkRun) {
	defer close(run.done)
	var copy gitrepo.Checkout
	copy, run.err = r.medium.repo.CopyForCheck(r.from, r.cache)
	if run.err != nil {
		return
	}
	defer copy.Remove()
	var box commandSandbox
	box, run.err = r.lp.openCommands(r.ctx, sandbox.Options{WorkDir: copy.Dir, Env: copy.Env, Read: r.medium.readable(), Loopback: r.medium.playbook.CheckLoopback})
	if run.err != nil {
		return
	}
	defer func() {
		if err := box.Close(); err != nil {
			if r.cleanupError != nil {
				r.cleanupError(err)
			} else {
				r.lp.commandCleanup(err)
			}
		}
	}()
	run.result, run.err = box.Run(r.ctx, sandbox.CommandRequest{Command: r.medium.playbook.Check})
}
func (r *checkRuns) close() {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	run := r.current
	r.mu.Unlock()
	if run != nil {
		<-run.done
	}
}

func boundedCommandResult(result sandbox.CommandResult) sandbox.CommandResult {
	var cut bool
	result.Stdout, cut = commandTail(result.Stdout, 24<<10)
	result.Truncated = result.Truncated || cut
	result.Stderr, cut = commandTail(result.Stderr, 8<<10)
	result.Truncated = result.Truncated || cut
	return result
}
func commandTail(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return "[earlier output omitted]\n" + s[len(s)-limit:], true
}

// JSON escapes can expand output sixfold. Keep the encoded reply below the
// hosted tool budget as well as bounding each stream's plain-text tail.
func commandResultJSON(result sandbox.CommandResult) (string, error) {
	result = boundedCommandResult(result)
	for {
		data, err := json.Marshal(result)
		if err != nil || len(data) <= 120<<10 {
			return string(data), err
		}
		result.Stdout, _ = commandTail(result.Stdout, len(result.Stdout)/2)
		result.Stderr, _ = commandTail(result.Stderr, len(result.Stderr)/2)
		result.Truncated = true
	}
}
