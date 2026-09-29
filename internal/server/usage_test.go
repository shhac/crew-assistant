package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

// The owner sees each engine's usage, or why there is none, and nobody
// else sees it at all. The CLIs here are missing, so no login is reached.
func TestUsageEndpoint(t *testing.T) {
	missing := t.TempDir()
	cfg := config.Default()
	cfg.Engines.Codex.Bin, cfg.Engines.Claude.Bin = filepath.Join(missing, "codex"), filepath.Join(missing, "claude")
	_, auth, h := newDashboard(t, cfg)
	get := func(owner bool) *httptest.ResponseRecorder {
		return send(h, auth, "GET", "/api/usage", nil, caller{owner: owner, csrf: owner})
	}
	if w := get(false); w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := get(true)
	var got []struct {
		Engine  string `json:"engine"`
		Level   string `json:"level"`
		Missing string `json:"missing"`
		Windows []any  `json:"windows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(got) != 2 || got[0].Engine != "codex" || got[1].Engine != "claude" {
		t.Fatal(w.Body.String())
	}
	for _, u := range got {
		if u.Level != "unknown" || u.Missing != "not installed" || u.Windows == nil || len(u.Windows) != 0 {
			t.Fatal(w.Body.String())
		}
	}
}
