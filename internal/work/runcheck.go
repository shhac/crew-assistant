package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/sandbox"

	"github.com/shhac/crew-assistant/internal/checktest"
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
	coverageNote func(string)
	current      *checkRun
	closed       bool
}
type checkRun struct {
	done       chan struct{}
	start      time.Time
	result     sandbox.CommandResult
	err        error
	delivered  bool
	progress   *checktest.Progress
	skipOffset int
	noted      bool
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
		defer r.mu.Unlock()
		if run.err != nil {
			run.delivered = true
			return "", fmt.Errorf("the check could not be run: %w", run.err)
		}
		content, next, err := checkResultJSON(run.result, run.progress, run.skipOffset)
		run.skipOffset = next
		run.delivered = run.progress == nil || next >= len(run.progress.Report.Skips)
		return content, err
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
	progressFile, err := os.CreateTemp(copy.Dir, ".crew-check-progress-")
	if err != nil {
		run.err = err
		return
	}
	progressPath := progressFile.Name()
	if err = progressFile.Close(); err != nil {
		run.err = err
		return
	}
	invocation := filepath.Base(progressPath)
	var box commandSandbox
	box, run.err = r.lp.openCommands(r.ctx, sandbox.Options{WorkDir: copy.Dir, Env: append(copy.Env, "CREW_HOSTED_CHECK=1", checktest.ProgressEnv+"="+progressPath, checktest.InvocationEnv+"="+invocation), Read: r.medium.readable(), Loopback: r.medium.playbook.CheckLoopback})
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
	run.progress, err = checktest.LoadProgress(copy.Dir, invocation, invocation)
	if err != nil {
		run.result.ExitCode = 1
		run.result.Stderr += "\nCheck coverage recovery failed; coverage incomplete: " + err.Error() + "\n"
	}
	if run.progress == nil && (r.ctx.Err() != nil || run.result.TimedOut) {
		run.progress = &checktest.Progress{Invocation: invocation, Stage: "check startup (no coverage checkpoint)", Report: checktest.Report{Failed: true}}
		run.result.Stderr += "\nNo coverage checkpoint was received; zero observed skips does not establish executed coverage.\n"
	}
	if run.progress != nil {
		if run.progress.Report.Failed && run.result.ExitCode == 0 {
			run.result.ExitCode = 1
		}
		if !run.progress.Done || run.result.TimedOut || r.ctx.Err() != nil || run.err != nil {
			run.progress.Done = false
			run.progress.Report.Complete = false
			run.progress.Report.Failed = true
			if run.result.ExitCode == 0 {
				run.result.ExitCode = 1
			}
			if run.err != nil {
				run.result.Stderr += "\nHosted check interrupted: " + run.err.Error() + "\n"
				run.err = nil
			}
		}
		// Append recovered evidence before the copy is removed. The structured
		// pages below retain every skip even when stdout itself must be bounded.
		if !run.progress.Done || run.result.Truncated {
			var recovered bytes.Buffer
			run.progress.Report.Write(&recovered)
			run.result.Stdout += "\nRecovered hosted coverage:\n" + recovered.String()
			run.result.Stdout += "Project check terminal stage: " + run.progress.Stage + "; coverage incomplete: " + fmt.Sprint(!run.progress.Done) + "\n"
		}
		if r.coverageNote != nil {
			status := "Hosted check finished."
			if r.ctx.Err() != nil || !run.progress.Done {
				status = "Hosted check interrupted; coverage incomplete."
			}
			r.noteCoverage(run.progress, status)
			r.mu.Lock()
			run.noted = true
			r.mu.Unlock()
		}
	}
}
func (r *checkRuns) close() {
	r.mu.Lock()
	if r.closed {
		run := r.current
		r.mu.Unlock()
		if run != nil {
			<-run.done
		}
		return
	}
	r.closed = true
	r.cancel()
	run := r.current
	r.mu.Unlock()
	if run != nil {
		<-run.done
		r.mu.Lock()
		unread := !run.delivered && run.noted
		r.mu.Unlock()
		if unread {
			r.coverageNote("Turn ended before all check evidence was read; full evidence was recorded when the hosted check finished.")
		}
	}
}

func (r *checkRuns) noteCoverage(p *checktest.Progress, status string) {
	if r.coverageNote == nil {
		return
	}
	required := 0
	for _, skip := range p.Report.Skips {
		if skip.Exception == "" {
			required++
		}
	}
	r.coverageNote(fmt.Sprintf("%s Project check stage: %s; observed skip count: %d; required-skip failures: %d", status, p.Stage, len(p.Report.Skips), required))
	if p.Report.EvidenceLimited {
		r.coverageNote("Skip evidence retention budget reached; collection stopped with all observed skips retained; coverage incomplete.")
	}
	var note bytes.Buffer
	flush := func() {
		if note.Len() > 0 {
			r.coverageNote(note.String())
			note.Reset()
		}
	}
	for _, skip := range p.Report.Skips {
		var entry bytes.Buffer
		skip.Write(&entry)
		// Stay below RecordTurnStep's 16 KiB text limit without splitting
		// an admitted identity or reason across notes.
		if note.Len()+entry.Len() > 12<<10 {
			flush()
		}
		note.Write(entry.Bytes())
	}
	flush()
	r.coverageNote(fmt.Sprintf("Go check observed skip count: %d; required-skip failures: %d; complete: %t", len(p.Report.Skips), required, p.Done && p.Report.Complete))
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
