package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/testbridge"
)

func TestBrowserBridgeStatus(t *testing.T) {
	binary, ownerHome, crew := testbridge.Fixture(t)
	for _, scenario := range []string{"usable", "undeclared", "missing-home", "missing-binary"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Default()
			cfg.Engines.Codex = config.CLIEngine{Bin: binary, Home: crew, BrowserBridgeHome: ownerHome}
			switch scenario {
			case "undeclared":
				cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
			case "missing-home":
				cfg.Engines.Codex.BrowserBridgeHome = filepath.Join(t.TempDir(), "absent")
			case "missing-binary":
				cfg.Engines.Codex.Bin = filepath.Join(t.TempDir(), "absent")
			}
			_, auth, handler := newDashboard(t, cfg)
			response := send(handler, auth, "GET", "/api/engines/codex/browser-bridge", nil, asOwner)
			var status browserBridgeStatus
			if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
				t.Fatal(err)
			}
			if status.Usable != (scenario == "usable") || status.Home != cfg.Engines.BridgeHome("codex") {
				t.Fatalf("%+v", status)
			}
			if scenario != "usable" && status.Reason == "" {
				t.Fatal("missing reason")
			}
			if scenario == "undeclared" && !strings.Contains(status.Reason, "declared") {
				t.Fatal(status.Reason)
			}
			if scenario == "missing-home" && !strings.Contains(status.Reason, "home is unavailable") {
				t.Fatal(status.Reason)
			}
		})
	}
	entries, err := os.ReadDir(ownerHome)
	if err != nil || len(entries) != 1 {
		t.Fatalf("owner home changed: %v %v", entries, err)
	}
}

func TestDefaultsIncludeBridgeHome(t *testing.T) {
	engines := configDefaults()["engines"].(map[string]engineDefaults)
	if engines["codex"].BridgeHome != (config.Engines{}).BridgeHome("codex") {
		t.Fatal(engines)
	}
}
