package work

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shhac/lib-agent-harness/sandbox"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

// start owns the app before QA is asked to judge it. All repository commands
// go through the standalone sandbox, even setup and readiness probes.
func (a *appRun) start(ctx context.Context, lp *Loop, read []string) {
	if !a.running() {
		return
	}
	defer func() {
		if a.unavailable != "" {
			a.stop()
		}
	}()
	fail := func(step string, err error) {
		a.unavailable = fmt.Sprintf("The app was not run: %s failed: %v. QA should judge the check alone.", step, err)
	}
	env := append(gitrepo.WorkspaceEnv(a.tree), "PORT="+strconv.Itoa(a.port))
	box, err := lp.openCommands(ctx, sandbox.Options{WorkDir: a.tree, Env: env, Read: read, Loopback: true})
	if err != nil {
		fail("opening the app sandbox", err)
		return
	}
	a.sandbox = box
	if a.recipe.Setup != "" {
		result, err := box.Run(ctx, sandbox.CommandRequest{Command: a.recipe.Setup, Timeout: 5 * time.Minute})
		if err != nil {
			fail("setup", err)
			return
		}
		if result.ExitCode != 0 || result.TimedOut {
			fail("setup", commandFailure(result))
			return
		}
	}
	a.log = filepath.Join(a.tree, ".crew-app.log")
	// The fixed log name is relative to the sandbox's workspace; the recipe
	// is run as its own shell group so redirects cover its entire output.
	a.process, err = box.Start(ctx, sandbox.CommandRequest{Command: "(\n" + a.recipe.Start + "\n) > .crew-app.log 2>&1"})
	if err != nil {
		fail("start", err)
		return
	}
	wait := 2 * time.Minute
	if lp.appWait > 0 {
		wait = lp.appWait
	}
	readyCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var last string
	for {
		select {
		case <-a.process.Done():
			result, err := a.process.Result()
			if err == nil {
				err = commandFailure(result)
			}
			fail("start (app exited before it was ready)", fmt.Errorf("%w; %s", err, appLogTail(a.log)))
			return
		default:
		}
		if readyCtx.Err() != nil {
			fail("readiness", fmt.Errorf("%w; %s; %s", readyCtx.Err(), last, appLogTail(a.log)))
			return
		}
		ready := false
		if a.recipe.Ready != "" {
			probeCtx, stop := context.WithTimeout(readyCtx, 5*time.Second)
			result, err := box.Run(probeCtx, sandbox.CommandRequest{Command: strings.ReplaceAll(a.recipe.Ready, core.PortPlaceholder, strconv.Itoa(a.port)), Timeout: 5 * time.Second})
			stop()
			ready = err == nil && result.ExitCode == 0 && !result.TimedOut
			if err != nil {
				last = err.Error()
			} else if !ready {
				last = commandFailure(result).Error()
			}
		} else {
			req, err := http.NewRequestWithContext(readyCtx, http.MethodGet, a.recipe.Address(a.port), nil)
			if err != nil {
				fail("readiness", err)
				return
			}
			client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(req)
			if err == nil {
				response.Body.Close()
				ready = true
			} else {
				last = err.Error()
			}
		}
		if ready {
			select {
			case <-a.process.Done():
				continue
			default:
				return
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-readyCtx.Done():
		case <-a.process.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
}

func commandFailure(result sandbox.CommandResult) error {
	r := boundedCommandResult(result)
	return fmt.Errorf("exit code %d, timed_out=%t, truncated=%t\n%s\n%s", r.ExitCode, r.TimedOut, r.Truncated, r.Stdout, r.Stderr)
}

func appLogTail(path string) string {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return ""
	}
	defer root.Close()
	entry, err := root.Lstat(filepath.Base(path))
	if err != nil || !entry.Mode().IsRegular() {
		return ""
	}
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	if info.Size() > 8<<10 {
		_, _ = f.Seek(-(8 << 10), io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, 8<<10))
	return string(data)
}

func (a *appRun) stop() {
	if a.process != nil {
		a.process.Stop()
		a.process = nil
	}
	if a.sandbox != nil {
		if err := a.sandbox.Close(); err != nil && a.cleanupError != nil {
			a.cleanupError(err)
		}
		a.sandbox = nil
	}
}

func (lp *Loop) commandCleanup(err error) {
	lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "command_cleanup"}, err)
}
