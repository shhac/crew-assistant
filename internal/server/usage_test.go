package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// The owner sees each engine's usage, or why there is none, and nobody
// else sees it at all. The CLIs here are missing, so no login is reached.
func TestUsageEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Engines.Codex.Bin, cfg.Engines.Claude.Bin = filepath.Join(dir, "codex"), filepath.Join(dir, "claude")
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	auth, _ := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	h := New(app.New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), app.Options{}), auth)
	get := func(owner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8340/api/usage", nil)
		r.RemoteAddr = "127.0.0.1:4321"
		if owner {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
			r.Header.Set("X-Requested-With", "crew-assistant")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
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
