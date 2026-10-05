//go:build !windows

package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"
)

// A subprocess gives the adapter's once-only toolchain discovery a synthetic
// PATH before its first lookup. No owner toolchain is changed or executed.
func TestRunCheckToolFromGrantedToolchain(t *testing.T) {
	if os.Getenv("CREW_TOOLCHAIN_FIXTURE") == "" {
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		if err := os.MkdirAll(bin, 0700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"go":                     "#!/bin/sh\ncase \"$2\" in GOROOT) printf '%s' \"$CREW_TOOLCHAIN_FIXTURE\";; GOMODCACHE) printf '%s' \"$CREW_TOOLCHAIN_FIXTURE/modules\";; *) exit 1;; esac\n",
			"crew-toolchain-fixture": "#!/bin/sh\nprintf 'granted-toolchain-check'\n",
		} {
			if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Mkdir(filepath.Join(root, "modules"), 0700); err != nil {
			t.Fatal(err)
		}
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestRunCheckToolFromGrantedToolchain$", "-test.count=1")
		cmd.Env = append(os.Environ(), "CREW_TOOLCHAIN_FIXTURE="+root, "PATH="+bin+":/usr/bin:/bin")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("toolchain check fixture: %v\n%s", err, output)
		}
		return
	}
	root, err := filepath.EvalSymlinks(os.Getenv("CREW_TOOLCHAIN_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	lp, m, source := checkFixture(t)
	for _, granted := range []bool{true, false} {
		t.Run(fmt.Sprint(granted), func(t *testing.T) {
			lp.commands = func(_ context.Context, opts sandbox.Options) (commandSandbox, error) {
				if !slices.Contains(opts.Read, root) {
					t.Fatalf("adapter did not grant discovered Go root: %v", opts.Read)
				}
				read := opts.Read
				if !granted {
					read = slices.DeleteFunc(slices.Clone(read), func(path string) bool { return path == root })
				}
				// Model the harness rule with a small synthetic system set. The fixture
				// root is outside it, so only the adapter's explicit grant admits its bin.
				var env []string
				dropped := false
				for _, entry := range opts.Env {
					if !strings.HasPrefix(entry, "PATH=") {
						env = append(env, entry)
						continue
					}
					var keep []string
					for _, path := range filepath.SplitList(strings.TrimPrefix(entry, "PATH=")) {
						path, resolveErr := filepath.EvalSymlinks(path)
						allowed := resolveErr == nil && (path == "/bin" || path == "/usr/bin" || path == "/usr")
						for _, dir := range read {
							rel, relErr := filepath.Rel(dir, path)
							if relErr == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
								allowed = true
							}
						}
						if allowed {
							keep = append(keep, path)
						} else {
							dropped = true
						}
					}
					env = append(env, "PATH="+strings.Join(keep, string(os.PathListSeparator)))
				}
				return &fakeCommands{run: func(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
					cmd := exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
					cmd.Dir, cmd.Env = opts.WorkDir, env
					var stdout, stderr strings.Builder
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					runErr := cmd.Run()
					result := sandbox.CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
					if dropped {
						result.Stderr += "\n[harness PATH: dropped outside read policy]"
					}
					var exit *exec.ExitError
					if errors.As(runErr, &exit) {
						result.ExitCode = exit.ExitCode()
						runErr = nil
					}
					return result, runErr
				}}, nil
			}
			result, _, err := lp.hostedCheck(context.Background(), m, source, "", "crew-toolchain-fixture", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if granted {
				if result.ExitCode != 0 || result.Stdout != "granted-toolchain-check" {
					t.Fatalf("%+v", result)
				}
			} else if result.ExitCode == 0 || !strings.Contains(result.Stderr, "[harness PATH:") {
				t.Fatalf("ungranted tool passed: %+v", result)
			}
		})
	}
}
