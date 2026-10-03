package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserBridgeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	owner, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	e := Engines{}
	if got := e.BridgeHome("codex"); got != filepath.Join(owner, ".codex") {
		t.Fatal(got)
	}
	e.Codex.BrowserBridgeHome = filepath.Join(home, "other")
	if got := e.BridgeHome("codex"); got != e.Codex.BrowserBridgeHome {
		t.Fatal(got)
	}
	if e.BridgeHome("claude") != "" {
		t.Fatal("Claude got bridge home")
	}
	for _, path := range []string{"relative", home + string(rune(0))} {
		cli := CLIEngine{BrowserBridgeHome: path}
		if cli.validate("engines.codex") == nil {
			t.Fatal("accepted", path)
		}
	}
}
