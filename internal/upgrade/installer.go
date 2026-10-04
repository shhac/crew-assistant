package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/releaseversion"
)

// HomebrewPrefix follows the executable rather than trusting PATH. Standalone
// installs deliberately have no self-install capability.
func HomebrewPrefix(executable string) (string, error) {
	real, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	bin := filepath.Dir(real)
	version := filepath.Dir(bin)
	formula := filepath.Dir(version)
	cellar := filepath.Dir(formula)
	if filepath.Base(real) != "crew-assistant" || filepath.Base(bin) != "bin" || filepath.Base(formula) != "crew-assistant" || filepath.Base(cellar) != "Cellar" {
		return "", errors.New("this executable is not a Homebrew crew-assistant install; upgrade by hand")
	}
	return filepath.Dir(cellar), nil
}

type CommandRunner func(context.Context, string, []string, []string) ([]byte, error)

// BrewInstaller admits an injected runner so tests never invoke real Homebrew.
type BrewInstaller struct {
	Run        CommandRunner
	ProbeBound time.Duration
}

func (i BrewInstaller) Install(ctx context.Context, prefix, formula string, env []string) (string, error) {
	run := i.Run
	if run == nil {
		run = func(ctx context.Context, name string, args, env []string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = env
			cmd.WaitDelay = 250 * time.Millisecond
			// Installer output can contain credentials from user hooks. Do not
			// include it in durable failures or owner-visible errors.
			var output versionOutput
			cmd.Stdout = &output
			cmd.Stderr = io.Discard
			err := cmd.Run()
			return output.data, err
		}
	}
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "HOMEBREW_NO_INSTALL_CLEANUP=") {
			clean = append(clean, entry)
		}
	}
	_, installErr := run(ctx, filepath.Join(prefix, "bin", "brew"), []string{"upgrade", formula}, clean)
	bound := i.ProbeBound
	if bound <= 0 {
		bound = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	output, probeErr := run(probeCtx, filepath.Join(prefix, "opt", "crew-assistant", "bin", "crew-assistant"), []string{"--version"}, clean)
	version := ""
	for _, word := range strings.Fields(string(output)) {
		if releaseversion.Valid(word) {
			version = word
			break
		}
	}
	if probeErr != nil || version == "" {
		if installErr != nil {
			return "", fmt.Errorf("%s; installed crew-assistant version could not be confirmed", installFailure(installErr))
		}
		return "", errors.New("installed crew-assistant version could not be confirmed")
	}
	if installErr != nil {
		return version, fmt.Errorf("%s; installed version is %s", installFailure(installErr), version)
	}
	return version, nil
}

// Never persist arbitrary command errors or output: user hooks can print
// secrets. Exit status and a manual retry give an actionable, safe failure.
func installFailure(err error) string {
	message := "Homebrew upgrade failed"
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		message += fmt.Sprintf(" (exit status %d)", exit.ExitCode())
	} else if errors.Is(err, context.Canceled) {
		message += " (cancelled)"
	} else if errors.Is(err, context.DeadlineExceeded) {
		message += " (timed out)"
	}
	return message + "; rerun the Homebrew upgrade command by hand to inspect its output"
}

// Retain only enough output for the version probe, without blocking a verbose
// installer once the limit is reached.
type versionOutput struct{ data []byte }

func (w *versionOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(w.data); remaining > 0 {
		w.data = append(w.data, p[:min(remaining, len(p))]...)
	}
	return n, nil
}
