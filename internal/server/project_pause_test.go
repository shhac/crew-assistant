package server

import (
	"encoding/json"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestProjectPauseEndpoint(t *testing.T) {
	_, call := ownerServer(t)
	created := call("POST", "/api/projects", `{"title":"Notes","brief":{"goal":"Write notes","criteria":["Clear"]},"template":"draft"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var p core.Project
	if err := json.Unmarshal(created.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"paused":true}`, `{"paused":false}`} {
		w := call("PUT", "/api/projects/"+p.ID+"/paused", body)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var got core.Project
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		want := body == `{"paused":true}`
		if got.Paused != want {
			t.Fatal(got)
		}
		w = call("GET", "/api/state", "")
		var snap core.Snapshot
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
			t.Fatal(err)
		}
		if snap.ProjectPaused(p.ID) != want {
			t.Fatal(snap.Projects)
		}
	}
	if w := call("PUT", "/api/projects/missing/paused", `{"paused":true}`); w.Code != 404 {
		t.Fatalf("unknown: %d %s", w.Code, w.Body.String())
	}
}
