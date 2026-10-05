package toolkit

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ActionInstall   = "install"
	ActionUpdate    = "update"
	ActionSkill     = "skill"
	ActionVerify    = "verify"
	ActionUpdateAll = "update-all"
)

var (
	ErrNotInstalled = errors.New("install it first")
	ErrChanged      = errors.New("the tools to update have changed; check the list again")
)

// plan is a job before it runs: the exact argv, built from the catalog, and
// how to describe the outcome once it ends. Its display is built from the
// same words the dashboard showed the owner, with only the program's path
// resolved.
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

// UpdateAll upgrades every outdated Homebrew tool in one run, but only the
// ones the owner confirmed: a list that changed since is refused.
func (m *Manager) UpdateAll(ctx context.Context, confirmed string) (JobView, error) {
	prefix := m.homebrewPrefix()
	if prefix == "" {
		return JobView{}, ErrNoHomebrew
	}
	words, names := updateAll(m.List(ctx, false).Tools, prefix)
	if len(names) == 0 {
		return JobView{}, ErrUpToDate
	}
	display := commandLine(words)
	if confirmed != display {
		return JobView{}, ErrChanged
	}
	return m.start(plan{
		action:  ActionUpdateAll,
		argv:    resolved(brewPath(prefix), words),
		display: display,
		env:     m.brewEnv(prefix),
		timeout: m.opts.InstallTimeout,
		finish: func(ctx context.Context, err error) (string, string) {
			if err == nil {
				return JobSucceeded, "Updated " + strings.Join(names, ", ") + "."
			}
			var now []string
			for _, name := range names {
				_, version := m.installedVersion(ctx, name, prefix)
				now = append(now, name+" "+version)
			}
			return JobFailed, exitText("Homebrew", err) + ". Now: " + strings.Join(now, ", ") + "."
		},
	})
}

// updateAll is the command that upgrades every outdated tool Homebrew
// itself installed; brew refuses to upgrade a copy it didn't install.
func updateAll(rows []Row, prefix string) (words, names []string) {
	words = []string{"brew", "upgrade"}
	for _, row := range rows {
		if row.Status == StatusOutdated && inPrefix(row.Path, prefix) {
			words = append(words, row.Formula)
			names = append(names, row.ID)
		}
	}
	return words, names
}

func (m *Manager) brewPlan(tool Tool, action string) (plan, error) {
	prefix := m.homebrewPrefix()
	if prefix == "" {
		return plan{}, ErrNoHomebrew
	}
	words := brewCommand(tool, action, inPrefix(m.binary(tool.ID, prefix), prefix))
	return plan{
		tool:    tool.ID,
		action:  action,
		argv:    resolved(brewPath(prefix), words),
		display: commandLine(words),
		env:     m.brewEnv(prefix),
		timeout: m.opts.InstallTimeout,
		finish: func(ctx context.Context, err error) (string, string) {
			_, version := m.installedVersion(ctx, tool.ID, prefix)
			return brewOutcome(tool.ID, version, err)
		},
	}, nil
}

// brewCommand installs a tool, and updates one by upgrading Homebrew's copy,
// or by installing Homebrew's beside one found elsewhere.
func brewCommand(tool Tool, action string, homebrews bool) []string {
	if action == ActionUpdate && homebrews {
		return []string{"brew", "upgrade", tool.Formula}
	}
	return []string{"brew", "install", tool.Formula}
}

func brewOutcome(id, version string, err error) (string, string) {
	installed := id + " " + version + " is installed."
	switch {
	case err == nil && version != "":
		return JobSucceeded, installed
	case err == nil:
		return JobFailed, "Homebrew finished, but " + id + "'s version couldn't be read."
	case version != "":
		return JobFailed, exitText("Homebrew", err) + "; " + installed
	}
	return JobFailed, exitText("Homebrew", err) + "."
}

func (m *Manager) skillPlan(tool Tool) (plan, error) {
	if tool.Skill == "" {
		return plan{}, ErrUnavailable
	}
	npx := m.npx(m.homebrewPrefix())
	if npx == "" {
		return plan{}, ErrNoNPX
	}
	words := skillRun(tool.Skill)
	return plan{
		tool:    tool.ID,
		action:  ActionSkill,
		argv:    resolved(npx, words),
		display: commandLine(words),
		env:     m.skillEnv(npx),
		timeout: m.opts.SkillTimeout,
		finish: func(_ context.Context, err error) (string, string) {
			return m.skillOutcome(tool.Skill, err)
		},
	}, nil
}

// skillRun passes --yes twice: once for npx fetching the skills CLI, once
// for the CLI's own prompts, since the daemon has no terminal to answer them.
func skillRun(name string) []string {
	return []string{"npx", "--yes", "skills", "add", skillsSource, "--skill", name, "--global", "--yes"}
}

func (m *Manager) skillOutcome(name string, err error) (string, string) {
	if err != nil {
		return JobFailed, exitText("The skills CLI", err) + "."
	}
	if !m.skillPresent(name) {
		return JobFailed, "The skills CLI finished, but the " + name + " skill isn't in ~/.claude/skills or ~/.agents/skills."
	}
	return JobSucceeded, "The " + name + " skill is installed."
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
		argv:    resolved(program, tool.Verify),
		display: commandLine(tool.Verify),
		env:     m.toolEnv(),
		timeout: m.opts.VerifyTimeout,
		finish: func(_ context.Context, err error) (string, string) {
			return verifyOutcome(tool.Verify[0], err)
		},
	}, nil
}

func verifyOutcome(program string, err error) (string, string) {
	if err == nil {
		return JobSucceeded, "Signed in."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return JobFailed, "The check timed out."
	}
	return JobFailed, "Not signed in yet (" + exitText(program, err) + "). Run the setup commands in your terminal."
}

// resolved is a command's words with its program replaced by the path found
// for it.
func resolved(program string, words []string) []string {
	return append([]string{program}, words[1:]...)
}

func brewPath(prefix string) string { return filepath.Join(prefix, "bin", "brew") }

func inPrefix(path, prefix string) bool {
	return path != "" && prefix != "" && filepath.Dir(path) == filepath.Join(prefix, "bin")
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
