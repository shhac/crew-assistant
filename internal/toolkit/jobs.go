package toolkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// A job's log is kept in memory only, and only this much of it.
const (
	logLines  = 400
	lineBytes = 1000
	keptJobs  = 8
)

// JobView is a job and the part of its log after a given line.
type JobView struct {
	ID         string     `json:"id"`
	Tool       string     `json:"tool,omitempty"`
	Action     string     `json:"action"`
	Command    string     `json:"command"`
	State      string     `json:"state"`
	Result     string     `json:"result,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Lines      []string   `json:"lines"`
	// Next is the line to read on from. Skipped counts lines that were
	// dropped from the log before they were read.
	Next    int `json:"next"`
	Skipped int `json:"skipped,omitempty"`
}

func (m *Manager) start(p plan) (JobView, error) {
	m.mu.Lock()
	if m.running != nil {
		m.mu.Unlock()
		return JobView{}, ErrBusy
	}
	j := &job{id: jobID(), tool: p.tool, action: p.action, command: p.display, state: JobRunning, started: m.opts.Now(), done: make(chan struct{})}
	m.running = j
	m.jobs = append(m.jobs, j)
	if len(m.jobs) > keptJobs {
		m.jobs = m.jobs[len(m.jobs)-keptJobs:]
	}
	m.mu.Unlock()
	go m.execute(j, p)
	return j.view(0), nil
}

func (m *Manager) execute(j *job, p plan) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	err := m.opts.Run(ctx, Command{Path: p.argv[0], Args: p.argv[1:], Env: p.env, Output: j})
	if ctx.Err() != nil && err != nil {
		err = errors.Join(ctx.Err(), err)
	}
	cancel()
	state, result := p.finish(context.Background(), err)
	j.end(state, result, m.opts.Now())
	m.mu.Lock()
	m.running = nil
	m.mu.Unlock()
	close(j.done)
}

// Job reads a job and its log after line after.
func (m *Manager) Job(id string, after int) (JobView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.id == id {
			return j.view(after), nil
		}
	}
	return JobView{}, ErrUnknownJob
}

func jobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type job struct {
	mu       sync.Mutex
	id       string
	tool     string
	action   string
	command  string
	state    string
	result   string
	started  time.Time
	finished time.Time
	lines    []string
	dropped  int
	partial  []byte
	done     chan struct{}
}

// Write keeps the log line by line, redacted and bounded. Installers can
// print credentials from the owner's own hooks, so nothing unredacted is
// kept, and nothing is kept past the daemon's run.
func (j *job) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			j.flush()
			continue
		}
		if len(j.partial) < lineBytes*4 {
			j.partial = append(j.partial, b)
		}
	}
	return len(p), nil
}

func (j *job) flush() {
	line := strings.TrimRight(strings.ToValidUTF8(string(j.partial), ""), " \t")
	j.partial = j.partial[:0]
	if line == "" {
		return
	}
	line = Redact(line)
	if len(line) > lineBytes {
		line = strings.ToValidUTF8(line[:lineBytes], "") + "…"
	}
	j.lines = append(j.lines, line)
	if len(j.lines) > logLines {
		j.dropped += len(j.lines) - logLines
		j.lines = j.lines[len(j.lines)-logLines:]
	}
}

func (j *job) end(state, result string, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.flush()
	j.state, j.result, j.finished = state, result, at
}

// summary is the job without its log.
func (j *job) summary() JobView {
	v := j.view(0)
	v.Lines, v.Skipped = []string{}, 0
	return v
}

func (j *job) view(after int) JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{ID: j.id, Tool: j.tool, Action: j.action, Command: j.command, State: j.state, Result: j.result, StartedAt: j.started, Next: j.dropped + len(j.lines)}
	if !j.finished.IsZero() {
		finished := j.finished
		v.FinishedAt = &finished
	}
	after = max(after, 0)
	if after < j.dropped {
		v.Skipped = j.dropped - after
		after = j.dropped
	}
	v.Lines = append([]string{}, j.lines[min(after-j.dropped, len(j.lines)):]...)
	return v
}
