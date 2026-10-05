// Package toolkit helps the owner install, update and sign in to the shhac
// CLI family and its skills from the dashboard. Every command it runs is
// built from its own catalog: nothing a request sends becomes part of a
// command line. It is an owner tool only; no model or role can reach it.
package toolkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/procgroup"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

const (
	StatusMissing   = "missing"
	StatusInstalled = "installed"
	StatusOutdated  = "outdated"
	StatusUnknown   = "unknown"
)

// LatestTTL is how long the tap's versions and the skills manifest are
// trusted before they are read again.
const LatestTTL = 6 * time.Hour

// HomebrewInstall is the official instruction, shown for the owner to run
// themselves; the daemon never runs install scripts.
const HomebrewInstall = `/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`

// Command is one process the toolkit runs, with its output going to Output.
type Command struct {
	Path   string
	Args   []string
	Env    []string
	Output io.Writer
}

type Runner func(context.Context, Command) error

type Options struct {
	// Run starts a process; tests inject a fake so nothing real is installed.
	Run Runner
	// Fetch reads a URL, such as a tap formula or the skills manifest.
	Fetch      func(context.Context, string) ([]byte, error)
	LookPath   func(string) (string, error)
	Executable func() (string, error)
	LookupEnv  func(string) (string, bool)
	Home       string
	// Prefixes are where Homebrew is looked for when the daemon itself was
	// not installed by it.
	Prefixes []string
	Now      func() time.Time
	// Timeouts bound each kind of job.
	InstallTimeout time.Duration
	SkillTimeout   time.Duration
	VerifyTimeout  time.Duration
	ProbeTimeout   time.Duration
}

type Manager struct {
	opts    Options
	refresh sync.Mutex // One read of the remote sources at a time.
	mu      sync.Mutex
	remote  remote
	jobs    []*job
	running *job
}

func New(opts Options) *Manager {
	if opts.Run == nil {
		opts.Run = run
	}
	if opts.Fetch == nil {
		client := &http.Client{Timeout: 30 * time.Second}
		opts.Fetch = func(ctx context.Context, url string) ([]byte, error) { return upgrade.Fetch(ctx, client, url) }
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Executable == nil {
		opts.Executable = os.Executable
	}
	if opts.LookupEnv == nil {
		opts.LookupEnv = os.LookupEnv
	}
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.Prefixes == nil {
		opts.Prefixes = []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.InstallTimeout = positive(opts.InstallTimeout, 15*time.Minute)
	opts.SkillTimeout = positive(opts.SkillTimeout, 5*time.Minute)
	opts.VerifyTimeout = positive(opts.VerifyTimeout, 30*time.Second)
	opts.ProbeTimeout = positive(opts.ProbeTimeout, 5*time.Second)
	return &Manager{opts: opts}
}

func positive(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

func run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	// Its own group keeps a terminal's Ctrl-C from killing an install half
	// way, and a timeout ends everything the installer started.
	procgroup.Detach(cmd)
	cmd.Env = c.Env
	cmd.WaitDelay = time.Second
	cmd.Stdout = c.Output
	cmd.Stderr = c.Output
	return cmd.Run()
}

var (
	ErrUnknownTool   = errors.New("there's no such tool in the toolkit")
	ErrUnknownAction = errors.New("there's no such toolkit action")
	ErrUnknownJob    = errors.New("there's no such toolkit job")
	ErrBusy          = errors.New("another tool is being set up; wait for it to finish")
	ErrNoHomebrew    = errors.New("Homebrew isn't installed; install it from brew.sh, then try again")
	ErrNoNPX         = errors.New("npx isn't available to crew-assistant; run the skill's command in your terminal")
	ErrUnavailable   = errors.New("this action isn't available for this tool")
	ErrUpToDate      = errors.New("every installed tool is up to date")
)

// Overview is what the dashboard shows: Homebrew, each tool and its skill,
// and the latest job.
type Overview struct {
	Homebrew  Homebrew   `json:"homebrew"`
	NPX       bool       `json:"npx"`
	Tools     []Row      `json:"tools"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Job       *JobView   `json:"job,omitempty"`
}

type Homebrew struct {
	Available bool   `json:"available"`
	Prefix    string `json:"prefix,omitempty"`
	Install   string `json:"install,omitempty"`
}

type Row struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Purpose       string    `json:"purpose"`
	Formula       string    `json:"formula"`
	Status        string    `json:"status"`
	Installed     string    `json:"installed,omitempty"`
	Latest        string    `json:"latest,omitempty"`
	Path          string    `json:"path,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Install       string    `json:"install"`
	Update        string    `json:"update"`
	Skill         *SkillRow `json:"skill,omitempty"`
	Setup         []string  `json:"setup"`
	Verify        bool      `json:"verify"`
	VerifyCommand string    `json:"verify_command,omitempty"`
	Connection    bool      `json:"connection"`
}

type SkillRow struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Installed string `json:"installed,omitempty"`
	Latest    string `json:"latest,omitempty"`
	// Command is for the owner's own terminal. Run is what the dashboard
	// runs instead, when npx can be found.
	Command  string `json:"command"`
	Runnable bool   `json:"runnable"`
	Run      string `json:"run,omitempty"`
}

// List reports every tool. A source that can't be read leaves its part
// unknown rather than failing the whole list.
func (m *Manager) List(ctx context.Context, refresh bool) Overview {
	m.ensureRemote(ctx, refresh)
	prefix := m.homebrewPrefix()
	m.mu.Lock()
	remote := m.remote
	var latest *JobView
	if n := len(m.jobs); n > 0 {
		view := m.jobs[n-1].summary()
		latest = &view
	}
	m.mu.Unlock()
	skills := m.readSkills()
	npx := m.npx(prefix)
	rows := make([]Row, len(catalog))
	var wg sync.WaitGroup
	for i, tool := range catalog {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = m.row(ctx, tool, prefix, remote, skills, npx != "")
		}()
	}
	wg.Wait()
	out := Overview{Homebrew: Homebrew{Available: prefix != "", Prefix: prefix}, NPX: npx != "", Tools: rows, Job: latest}
	if prefix == "" {
		out.Homebrew.Install = HomebrewInstall
	}
	if !remote.checked.IsZero() {
		checked := remote.checked
		out.CheckedAt = &checked
	}
	return out
}

func (m *Manager) row(ctx context.Context, tool Tool, prefix string, remote remote, skills skillState, npx bool) Row {
	r := Row{ID: tool.ID, Name: tool.Name, Purpose: tool.Purpose, Formula: tool.Formula, Install: "brew install " + tool.Formula, Update: "brew upgrade " + tool.Formula, Setup: append([]string{}, tool.Setup...), Verify: len(tool.Verify) > 0, Connection: tool.Connection, Latest: remote.versions[tool.ID]}
	if r.Verify {
		r.VerifyCommand = commandLine(tool.Verify)
	}
	if tool.Skill != "" {
		skill := m.skillRow(tool.Skill, remote, skills)
		if npx {
			skill.Runnable, skill.Run = true, commandLine(append([]string{"npx"}, skillArgs(tool.Skill)...))
		}
		r.Skill = &skill
	}
	r.Path = m.binary(tool.ID, prefix)
	if r.Path == "" {
		r.Status = StatusMissing
		return r
	}
	if prefix == "" || filepath.Dir(r.Path) != filepath.Join(prefix, "bin") {
		r.Detail = "Found outside Homebrew; Update installs Homebrew's copy beside it."
	}
	r.Installed = m.probeVersion(ctx, r.Path)
	r.Status = versionStatus(r.Installed, r.Latest)
	if r.Installed == "" {
		r.Detail = "Its version couldn't be read."
	}
	return r
}
