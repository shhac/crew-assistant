package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestModelLoginUsesConfiguredHomeWithoutChangingProcessEnvironment(t *testing.T) {
	ambient := t.TempDir()
	t.Setenv("CODEX_HOME", ambient)
	t.Setenv("OPENAI_API_KEY", "must-not-be-forwarded")
	profile := config.Default().Model
	profile.CodexHome = filepath.Join(t.TempDir(), "private-login")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	profile.CodexBin = binary
	child, err := prepareModelLogin(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Args) != 2 || child.Args[1] != "login" || child.Dir != profile.CodexHome {
		t.Fatal(child.Args, child.Dir)
	}
	matched := false
	for _, value := range child.Env {
		if value == "CODEX_HOME="+profile.CodexHome {
			matched = true
		}
		if strings.HasPrefix(value, "OPENAI_API_KEY=") || value == "CODEX_HOME="+ambient {
			t.Fatal("inherited wrong login environment")
		}
	}
	if !matched || os.Getenv("CODEX_HOME") != ambient {
		t.Fatal("configured home missing or global environment mutated")
	}
	info, err := os.Stat(profile.CodexHome)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
	// The process is intentionally never started: no account or model is contacted.
}

func TestModelLoginRejectsGlobalInstructionsInConfiguredHome(t *testing.T) {
	profile := config.Default().Model
	profile.CodexHome = t.TempDir()
	profile.CodexBin, _ = os.Executable()
	if err := os.WriteFile(filepath.Join(profile.CodexHome, "AGENTS.md"), []byte("Project coding instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareModelLogin(context.Background(), profile); err == nil {
		t.Fatal("global instructions accepted")
	}
}

func TestClaudeLoginUsesConfiguredHomeAndSubscription(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-forwarded")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	profile := config.Default().WorkerModel
	profile.Engine = "claude"
	profile.ClaudeHome = filepath.Join(t.TempDir(), "worker-login")
	profile.ClaudeBin, _ = os.Executable()
	child, err := prepareModelLogin(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(child.Args[1:], " ") != "auth login --claudeai" || child.Dir != profile.ClaudeHome {
		t.Fatal(child.Args, child.Dir)
	}
	found := false
	for _, value := range child.Env {
		if value == "CLAUDE_CONFIG_DIR="+profile.ClaudeHome {
			found = true
		}
		if strings.HasPrefix(value, "ANTHROPIC_API_KEY=") {
			t.Fatal("inherited API billing credential")
		}
	}
	if !found {
		t.Fatal("configured login home missing")
	}
	// Never start the process: login remains an explicit owner action.
}
