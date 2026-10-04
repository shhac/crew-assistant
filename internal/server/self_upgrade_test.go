package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func TestAskUpgradeAPIAndConcurrentRequests(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	e := &upgrade.Engine{Path: upgrade.RecordPath(filepath.Join(dir, "state.db")), Drain: func() bool { return true }}
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), app.Options{Version: "v1.0.0", UpgradeEngine: e, RequestUpgrade: func(version string, automatic bool) error {
		return e.Request(upgrade.Record{From: "v1.0.0", To: version, Automatic: automatic})
	}})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	if err = s.RecordUpdateCheck(context.Background(), "v1.0.0", upgrade.Result{Available: "v2.0.0"}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(context.Background())
	d := snap.Decisions[0]
	body, _ := json.Marshal(map[string]string{"choice": core.UpgradeChoice("v2.0.0")})
	w := send(h, auth, http.MethodPost, "/api/decisions/"+d.ID+"/resolve", strings.NewReader(string(body)), asOwner)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = send(h, auth, http.MethodPost, "/api/upgrade", nil, asOwner)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "an upgrade is already under way") {
		t.Fatal(w.Code, w.Body.String())
	}
	snap, _ = s.Snapshot(context.Background())
	if snap.Update.Skipped != "" {
		t.Fatal("upgrade choice skipped release")
	}
	a.SetUpgrading(true)
	w = send(h, auth, http.MethodPost, "/api/projects", strings.NewReader(`{"title":"Held"}`), asOwner)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "upgrading") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = send(h, auth, http.MethodGet, "/api/state", nil, asOwner)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	r, _ := upgrade.ReadRecord(e.Path)
	r.Step, r.Pinned, r.Failure, r.StepAt = upgrade.RolledBack, true, "API failed", time.Now()
	if err = upgrade.WriteRecord(e.Path, *r); err != nil {
		t.Fatal(err)
	}
	w = send(h, auth, http.MethodGet, "/api/state", nil, asOwner)
	if !strings.Contains(w.Body.String(), `"rollback":`) || !strings.Contains(w.Body.String(), "upgrade clear-rollback") {
		t.Fatal(w.Body.String())
	}
}
