package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/testutil"
	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"
)

// A runtime directory and records left readable by others, say by an older
// version or a hand edit, are made private again when the daemon starts.
func TestServeTightensRuntimeFilesLeftOpen(t *testing.T) {
	testutil.RequireLoopback(t)
	dir := t.TempDir()
	o := &options{configPath: filepath.Join(dir, "config.json"), statePath: filepath.Join(dir, "state.db"), globals: &libcli.Globals{Format: string(output.FormatNDJSON)}}
	runtimeDir := o.runtimeDir()
	daemon := filepath.Join(runtimeDir, "daemon.json")
	token := filepath.Join(runtimeDir, "admin-token")
	if err := os.Mkdir(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{daemon, token} {
		if err := os.WriteFile(p, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for p, mode := range map[string]os.FileMode{runtimeDir: 0755, daemon: 0644, token: 0644} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Dashboard.Addr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	// Startup resolves paths; the client must not share those mutable options.
	daemonOptions := *o
	go func() { done <- serve(lifecycle.Now(ctx), &daemonOptions, cfg, true, "", false, true) }()
	deadline := time.After(5 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for ready := false; !ready; {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before readiness: %v", err)
		case <-deadline:
			t.Fatal("daemon did not rewrite its runtime record")
		case <-poll.C:
			info, err := os.Stat(daemon)
			ready = err == nil && info.Mode().Perm() == 0600
		}
	}
	for p, want := range map[string]os.FileMode{runtimeDir: 0700, token: 0600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", p, got, want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
