package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestEnginePauseEndpoint(t *testing.T) {
	cfg := config.Default()
	zero := 0
	cfg.Engines.Codex.UsageFloor = config.UsageFloor{FiveHourPercent: &zero, WeekPercent: &zero}
	cfg.Engines.Claude.UsageFloor = cfg.Engines.Codex.UsageFloor
	cfg.Engines.Grok.UsageFloor = cfg.Engines.Codex.UsageFloor
	_, auth, h := newDashboard(t, cfg)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return send(h, auth, method, path, strings.NewReader(body), asOwner)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, body := range []string{`{"paused":true,"until":"` + until + `"}`, `{"paused":false}`} {
		w := call("PUT", "/api/engines/claude/paused", body)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var stored struct {
			Engine string     `json:"engine"`
			State  string     `json:"state"`
			Until  *time.Time `json:"until"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Engine != "claude" || (stored.State == "paused") != strings.Contains(body, "true") {
			t.Fatal(w.Body.String())
		}
		if strings.Contains(body, "true") && (stored.Until == nil || stored.Until.Format(time.RFC3339) != until) {
			t.Fatal(stored)
		}
		state := call("GET", "/api/state", "")
		var snap core.Snapshot
		if err := json.Unmarshal(state.Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
		_, paused := snap.EnginePaused("claude", time.Now())
		if paused != strings.Contains(body, "true") {
			t.Fatal(snap.EnginePauses)
		}
		status := call("GET", "/api/engines", "")
		if status.Code != 200 || !strings.Contains(status.Body.String(), `"engine":"claude"`) {
			t.Fatal(status.Code, status.Body.String())
		}
	}
	for _, test := range []struct{ engine, body string }{
		{"missing", `{"paused":true}`}, {"claude", `{"paused":true,"until":"2000-01-01T00:00:00Z"}`},
	} {
		if w := call("PUT", "/api/engines/"+test.engine+"/paused", test.body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestEnginePauseEndpointRequiresOwnerAndCSRF(t *testing.T) {
	_, auth, h := newDashboard(t, config.Default())
	for _, as := range []caller{{}, {owner: true}, {csrf: true}} {
		w := send(h, auth, "PUT", "/api/engines/claude/paused", strings.NewReader(`{"paused":true}`), as)
		if w.Code < 400 {
			t.Fatal("unauthorized accepted", as, w.Code)
		}
	}
	w := send(h, auth, "GET", "/api/engines", nil, caller{})
	if w.Code < 400 {
		t.Fatal("anonymous status accepted")
	}
}
