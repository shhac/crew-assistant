package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/testutil"
	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"
)

func TestDemoShutdownReleasesStateAndRuntimeRecord(t *testing.T) {
	testutil.RequireLoopback(t)
	dir := t.TempDir()
	o := &options{configPath: filepath.Join(dir, "config.json"), statePath: filepath.Join(dir, "state.db"), globals: &libcli.Globals{Format: string(output.FormatNDJSON)}}
	cfg := config.Default()
	cfg.Dashboard.Addr = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(lifecycle.Now(ctx), o, cfg, true, "", false, true) }()
	deadline := time.After(5 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before readiness: %v", err)
		case <-deadline:
			t.Fatal("daemon did not become ready")
		case <-poll.C:
			if _, err := os.Stat(filepath.Join(o.runtimeDir(), "daemon.json")); err == nil {
				goto ready
			}
		}
	}
ready:
	v, err := o.request("GET", "/api/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var changed config.Config
	if err = json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Assistant.Name = "Demo-only name"
	if _, err = o.request("PUT", "/api/config", changed); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(o.configPath); !os.IsNotExist(err) {
		t.Fatal("demo changed real configuration")
	}
	if _, err = os.Stat(filepath.Join(o.runtimeDir(), "demo-config.json")); err != nil {
		t.Fatal("demo preferences were not saved in isolated state")
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
	if _, err := os.Stat(filepath.Join(o.runtimeDir(), "daemon.json")); !os.IsNotExist(err) {
		t.Fatalf("runtime marker survived shutdown: %v", err)
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil || !ok {
		t.Fatalf("state lock survived shutdown: %v", err)
	}
	_ = lock.Unlock()
}

func TestOnlyARealStartRewritesAnEarlierConfig(t *testing.T) {
	old := `{"model":{"engine":"claude","model":"opus","effort":"","max_tokens":4096,"claude_home":"/synthetic/claude"}}`
	for _, demo := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(old), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadServeConfig(path, demo)
		if err != nil || cfg.Engines.Claude.Home != "/synthetic/claude" {
			t.Fatalf("demo %v: %+v %v", demo, cfg.Engines, err)
		}
		data, _ := os.ReadFile(path)
		if rewritten := string(data) != old; rewritten == demo {
			t.Fatalf("demo %v rewrote the file: %v\n%s", demo, rewritten, data)
		}
	}
}
