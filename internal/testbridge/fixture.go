// Package testbridge provides a synthetic, offline Codex bridge for tests.
package testbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func Fixture(t *testing.T) (binary, owner, crew string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	owner, crew = t.TempDir(), t.TempDir()
	install := t.TempDir()
	for _, name := range []string{"node_repl", "node", "browser-service.mjs"} {
		if err := os.WriteFile(filepath.Join(install, name), []byte("synthetic"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	services, _ := json.Marshal(map[string]string{"browser": filepath.Join(install, "browser-service.mjs")})
	raw, _ := json.Marshal(map[string]any{"name": "node_repl", "enabled": true, "transport": map[string]any{"type": "stdio", "command": filepath.Join(install, "node_repl"), "env": map[string]string{
		"NODE_REPL_NODE_PATH": filepath.Join(install, "node"), "NODE_REPL_NODE_MODULE_DIRS": install, "NODE_REPL_TRUSTED_SERVICES": string(services),
	}}})
	if err := os.WriteFile(filepath.Join(owner, "bridge.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	binary = filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n[ \"$*\" = \"mcp get node_repl --json\" ] || exit 9\ncat \"$CODEX_HOME/bridge.json\" 2>/dev/null\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return
}
