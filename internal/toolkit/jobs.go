package toolkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ActionInstall   = "install"
	ActionUpdate    = "update"
	ActionSkill     = "skill"
	ActionVerify    = "verify"
	ActionUpdateAll = "update-all"
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

var ErrNotInstalled = errors.New("install it first")

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

// plan is a job before it runs: the exact argv, built from the catalog, and
// how to describe the outcome once it ends.
type plan struct {
	tool    string
	action  string
	argv    []string
	display string
	env     []string
	timeout time.Duration
	finish  func(context.Context, error) (state, result string)
}

// Start runs one action for one tool, unless a job is already running.
func (m *Manager) Start(id, action string) (JobView, error) {
	tool, ok := lookup(id)
	if !ok {
		return JobView{}, ErrUnknownTool
	}
	var p plan
	var err error
	switch action {
	case ActionInstall, ActionUpdate:
		p, err = m.brewPlan(tool, action)
	case ActionSkill:
		p, err = m.skillPlan(tool)
	case ActionVerify:
		p, err = m.verifyPlan(tool)
	default:
		return JobView{}, ErrUnknownAction
	}
	if err != nil {
		return JobView{}, err
	}
	return m.start(p)
}

// UpdateAll upgrades every installed tool that has a newer release, in one
// Homebrew run.
func (m *Manager) UpdateAll(ctx context.Context) (JobView, error) {
	prefix := m.homebrewPrefix()
	if prefix == "" {
		return JobView{}, ErrNoHomebrew
	}
	var outdated []Tool
	for _, row := range m.List(ctx, false).Tools {
		if row.Status == StatusOutdated {
			tool, _ := lookup(row.ID)
			outdated = append(outdated, tool)
		}
	}
	if len(outdated) == 0 {
		return JobView{}, ErrUpToDate
	}
	argv := []string{filepath.Join(prefix, "bin", "brew"), "upgrade"}
	var names []string
	for _, tool := range outdated {
		argv = append(argv, tool.Formula)
		names = append(names, tool.ID)
	}
	return m.start(plan{
		action:  ActionUpdateAll,
		argv:    argv,
		display: commandLine(append([]string{"brew"}, argv[1:]...)),
		env:     m.brewEnv(prefix),
		timeout: m.opts.InstallTimeout,
		finish: func(ctx context.Context, err error) (string, string) {
			var current []string
			for _, tool := range outdated {
				if path := m.binary(tool.ID, prefix); path != "" {
					current = append(current, tool.ID+" "+m.probeVersion(ctx, path))
				}
			}
			if err != nil {
				return JobFailed, exitText("Homebrew", err) + ". Now: " + strings.Join(current, ", ") + "."
			}
			return JobSucceeded, "Updated " + strings.Join(names, ", ") + "."
		},
	})
}

func (m *Manager) brewPlan(tool Tool, action string) (plan, error) {
	prefix := m.homebrewPrefix()
	if prefix == "" {
		return plan{}, ErrNoHomebrew
	}
	verb := "install"
	if action == ActionUpdate {
		verb = "upgrade"
	}
	return plan{
		tool:    tool.ID,
		action:  action,
		argv:    []string{filepath.Join(prefix, "bin", "brew"), verb, tool.Formula},
		display: "brew " + verb + " " + tool.Formula,
		env:     m.brewEnv(prefix),
		timeout: m.opts.InstallTimeout,
		finish: func(ctx context.Context, err error) (string, string) {
			version := ""
			if path := m.binary(tool.ID, prefix); path != "" {
				version = m.probeVersion(ctx, path)
			}
			installed := tool.ID + " " + version + " is installed."
			switch {
			case err == nil && version != "":
				return JobSucceeded, installed
			case err == nil:
				return JobFailed, "Homebrew finished, but " + tool.ID + "'s version couldn't be read."
			case version != "":
				return JobFailed, exitText("Homebrew", err) + "; " + installed
			}
			return JobFailed, exitText("Homebrew", err) + "."
		},
	}, nil
}

func (m *Manager) skillPlan(tool Tool) (plan, error) {
	if tool.Skill == "" {
		return plan{}, ErrUnavailable
	}
	npx := m.npx(m.homebrewPrefix())
	if npx == "" {
		return plan{}, ErrNoNPX
	}
	args := skillArgs(tool.Skill)
	env := append(m.environment("HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"),
		"PATH="+strings.Join([]string{filepath.Dir(npx), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"))
	return plan{
		tool:    tool.ID,
		action:  ActionSkill,
		argv:    append([]string{npx}, args...),
		display: commandLine(append([]string{"npx"}, args...)),
		env:     env,
		timeout: m.opts.SkillTimeout,
		finish: func(_ context.Context, err error) (string, string) {
			return m.skillOutcome(tool.Skill, err)
		},
	}, nil
}

// skillArgs passes --yes twice: once for npx fetching the skills CLI, once
// for the CLI's own prompts, since the daemon has no terminal to answer them.
func skillArgs(name string) []string {
	return []string{"--yes", "skills", "add", skillsSource, "--skill", name, "--global", "--yes"}
}

func (m *Manager) skillOutcome(name string, err error) (string, string) {
	if err == nil && m.skillPresent(name) {
		return JobSucceeded, "The " + name + " skill is installed."
	}
	if err != nil {
		return JobFailed, exitText("The skills CLI", err) + "."
	}
	return JobFailed, "The skills CLI finished, but the " + name + " skill isn't in ~/.claude/skills or ~/.agents/skills."
}

func (m *Manager) verifyPlan(tool Tool) (plan, error) {
	if len(tool.Verify) == 0 {
		return plan{}, ErrUnavailable
	}
	prefix := m.homebrewPrefix()
	if m.binary(tool.ID, prefix) == "" {
		return plan{}, fmt.Errorf("%s isn't installed; %w", tool.ID, ErrNotInstalled)
	}
	program := m.binary(tool.Verify[0], prefix)
	if program == "" {
		return plan{}, fmt.Errorf("%s isn't installed; %w", tool.Verify[0], ErrNotInstalled)
	}
	return plan{
		tool:    tool.ID,
		action:  ActionVerify,
		argv:    append([]string{program}, tool.Verify[1:]...),
		display: commandLine(tool.Verify),
		env:     m.toolEnv(),
		timeout: m.opts.VerifyTimeout,
		finish: func(_ context.Context, err error) (string, string) {
			if err == nil {
				return JobSucceeded, "Signed in."
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return JobFailed, "The check timed out."
			}
			return JobFailed, "Not signed in yet (" + exitText(tool.Verify[0], err) + "). Run the setup commands in your terminal."
		},
	}, nil
}

// brewEnv gives Homebrew what it needs and none of the daemon's own
// settings or credentials.
func (m *Manager) brewEnv(prefix string) []string {
	env := m.environment("HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "SHELL")
	return append(env, "PATH="+strings.Join([]string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, ":"), "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_COLOR=1")
}

func exitText(who string, err error) string {
	var exit *exec.ExitError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return who + " timed out"
	case errors.Is(err, context.Canceled):
		return who + " was cancelled"
	case errors.As(err, &exit):
		return fmt.Sprintf("%s exited with status %d", who, exit.ExitCode())
	}
	return who + " couldn't run"
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
