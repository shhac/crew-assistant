//go:build !windows

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProductionVersionProbeBoundsDescendantPipes(t *testing.T) {
	prefix := t.TempDir()
	pidFile := filepath.Join(prefix, "child.pid")
	brew := filepath.Join(prefix, "bin", "brew")
	binary := filepath.Join(prefix, "opt", "crew-assistant", "bin", "crew-assistant")
	for _, path := range []string{brew, binary} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(brew, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n/bin/sleep 20 &\necho $! > '" + pidFile + "'\nwait\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	defer func() {
		b, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	started := time.Now()
	if _, err := (BrewInstaller{ProbeBound: 50 * time.Millisecond}).Install(context.Background(), prefix, "fixture", os.Environ()); err == nil {
		t.Fatal("hanging binary accepted")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("descendant held probe pipes beyond bound")
	}
}
