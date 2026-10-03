package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/roles"
)

type browserBridgeStatus struct {
	Usable bool   `json:"usable"`
	Reason string `json:"reason"`
	Home   string `json:"home"`
}

func browserBridgeHandler(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := a.Config()
		status := browserBridgeStatus{Home: cfg.Engines.BridgeHome("codex")}
		defer func() { respond(w, http.StatusOK, status) }()
		if !config.Supports("codex", config.UseBrowser) {
			status.Reason = "Codex browser use is not supported on this platform."
			return
		}
		binary, home := cfg.Engines.Binary("codex")
		if _, err := exec.LookPath(binary); err != nil {
			status.Reason = "The configured Codex program could not be found."
			return
		}
		parent := filepath.Join(a.Core.StateDirectory(), "roles")
		if err := os.MkdirAll(parent, 0700); err != nil {
			status.Reason = "The bridge check workspace is unavailable."
			return
		}
		dir, err := os.MkdirTemp(parent, "browser-check-")
		if err != nil {
			status.Reason = "The bridge check workspace is unavailable."
			return
		}
		defer os.RemoveAll(dir)
		err = roles.CheckBrowserBridge(r.Context(), roles.Spec{Engine: "codex", Binary: binary, Home: home, BridgeHome: status.Home, RuntimeHome: filepath.Join(parent, "codex"), WorkDir: dir, Browser: true})
		if err == nil {
			status.Usable = true
			return
		}
		if reason, ok := roles.BrowserUnusable(err, status.Home); ok {
			status.Reason = reason
		} else {
			status.Reason = "The browser bridge check could not complete."
		}
	}
}
