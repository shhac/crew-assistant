package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/shhac/crew-assistant/internal/procgroup"
)

// validBranch refuses anything git would not take as a branch name, before it
// reaches a ref or a refspec.
func validBranch(ctx context.Context, dir, name string) error {
	if _, err := run(ctx, dir, "check-ref-format", "--branch", name); err != nil {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	return nil
}

// fetchQuietly is every fetch the daemon makes: no tags, no submodules, no
// housekeeping.
var fetchQuietly = []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-auto-gc"}

// run is the only way this package runs git. Hooks, fsmonitor and system
// configuration are off for every command, wherever it runs.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	return runIn(ctx, dir, gitEnvironment(), args...)
}

// runIn is run with a chosen environment, for the commands that sign as the
// owner would.
func runIn(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := append(append([]string(nil), safety...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	procgroup.Detach(cmd)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 300 {
			detail = detail[:300]
		}
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		return stdout.String(), &gitError{command: args[0], detail: detail, code: code}
	}
	return stdout.String(), nil
}

type gitError struct {
	command, detail string
	code            int
}

func (e *gitError) Error() string { return "git " + e.command + ": " + e.detail }

// safety is prepended to every git command: nothing configured in a
// repository, the operator's global config or the system can make git run a
// program as the daemon.
var safety = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "core.attributesFile=/dev/null",
	"-c", "core.excludesFile=/dev/null",
	"-c", "core.sshCommand=false",
	"-c", "gc.auto=0",
	"-c", "maintenance.auto=false",
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "diff.external=",
}

// gitEnvironment keeps the process's ordinary environment but none of its GIT_
// settings, and ignores global and system git configuration.
func gitEnvironment() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		// Refusals are read from git's own words.
		"LC_ALL=C",
	)
}

// ChangedFiles counts the files in dir's working tree that differ from its
// last commit, new ones included: how far a round of work has got.
func ChangedFiles(ctx context.Context, dir string) (int, error) {
	out, err := run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, entry := range strings.Split(out, "\x00") {
		// A rename names its source after it, as an entry of its own
		// without the two status letters.
		if len(entry) > 3 && entry[2] == ' ' {
			n++
		}
	}
	return n, nil
}
