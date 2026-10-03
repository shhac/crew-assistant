package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestDashboardUpdateModePersistsAndSnapshotCarriesStatus(t *testing.T) {
	_, call := ownerApp(t)
	for _, mode := range []string{"off", "ask"} {
		c := config.Default()
		c.Upgrade.Mode = mode
		body, _ := json.Marshal(c)
		if w := call("PUT", "/api/config", string(body)); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var saved config.Config
		w := call("GET", "/api/config", "")
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.Upgrade.Mode != mode {
			t.Fatal(saved.Upgrade, err)
		}
		w = call("GET", "/api/state", "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"`+mode+`"`) || !strings.Contains(w.Body.String(), "development build") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	c := config.Default()
	c.Upgrade.Mode = "auto"
	body, _ := json.Marshal(c)
	if w := call("PUT", "/api/config", string(body)); w.Code != 400 || !strings.Contains(w.Body.String(), "available yet") {
		t.Fatal(w.Code, w.Body.String())
	}
}
