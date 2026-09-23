package app

import (
	"os"
	"os/exec"

	"github.com/shhac/crew-assistant/internal/config"
)

func modelAvailable(m config.Model) bool {
	if m.Model == "" {
		return false
	}
	if m.Engine == "claude" {
		_, err := exec.LookPath(m.ClaudeBin)
		return err == nil
	}
	if m.Engine == "codex" {
		_, err := exec.LookPath(m.CodexBin)
		return err == nil
	}
	return m.APIKeyEnv == "" || os.Getenv(m.APIKeyEnv) != ""
}
