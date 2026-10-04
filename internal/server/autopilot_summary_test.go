package server

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestAutopilotSummaryAndDigestOwnerRoutes(t *testing.T) {
	_, call := ownerApp(t)
	for _, path := range []string{"/api/autopilot/summary", "/api/autopilot/digests"} {
		if out := call("GET", path, ""); out.Code != 200 {
			t.Fatalf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
	for _, path := range []string{"/api/autopilot/summary?after=-1", "/api/autopilot/summary?after=100", "/api/autopilot/summary?after=bad", "/api/autopilot/summary?extra=true", "/api/autopilot/summary?after=0&after=0", "/api/autopilot/summary?after=%ZZ", "/api/autopilot/digests?before=bad", "/api/autopilot/digests?limit=201"} {
		if out := call("GET", path, ""); out.Code != 400 {
			t.Fatalf("%s: %d", path, out.Code)
		}
	}
	for _, body := range []string{`{}`, `{"boundary":1}`, `{"boundary":-1}`, `{"boundary":0,"actor":"owner"}`} {
		if out := call("POST", "/api/autopilot/summary/ack", body); out.Code != 400 {
			t.Fatalf("%s: %d", body, out.Code)
		}
	}
	if out := call("POST", "/api/autopilot/summary/ack", `{"boundary":0}`); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	for _, body := range []string{`{}`, `{"enabled":true,"at":"25:00","revision":0}`, `{"enabled":true,"revision":0,"actor":"assistant"}`} {
		if out := call("PUT", "/api/autopilot/digest", body); out.Code != 400 {
			t.Fatalf("%s: %d", body, out.Code)
		}
	}
	if out := call("PUT", "/api/autopilot/digest", `{"enabled":true,"at":"09:30","revision":0}`); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if out := call("PUT", "/api/autopilot/digest", `{"enabled":false,"revision":0}`); out.Code != 409 {
		t.Fatalf("stale digest accepted: %d", out.Code)
	}
}

func TestAutopilotSummaryStorageErrorsStayServerErrors(t *testing.T) {
	a, call := ownerApp(t)
	db, err := sql.Open("sqlite", filepath.Join(a.Core.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"autopilot_audit", "autopilot_digests"} {
		if _, err := db.Exec("DROP TABLE " + table); err != nil {
			t.Fatal(err)
		}
	}
	for _, route := range []struct{ method, path, body string }{{"GET", "/api/autopilot/summary", ""}, {"POST", "/api/autopilot/summary/ack", `{"boundary":0}`}, {"GET", "/api/autopilot/digests", ""}} {
		if out := call(route.method, route.path, route.body); out.Code != 500 {
			t.Fatalf("storage failure became %d: %s", out.Code, out.Body.String())
		}
	}
}

func TestFilteredSummaryCannotAcknowledgeGlobalProgress(t *testing.T) {
	a, call := ownerApp(t)
	p, err := a.Core.CreateProject(t.Context(), core.ProjectInput{Title: "One project", Brief: core.BriefInput{Goal: "Synthetic", Criteria: []string{"Synthetic"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	for _, filtered := range []bool{false, true} {
		path := "/api/autopilot/summary"
		if filtered {
			path += "?project_id=" + p.ID
		}
		response := call("GET", path, "")
		var summary core.AutopilotSummary
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Acknowledgeable == filtered {
			t.Fatal("wrong presentation acknowledgement scope", filtered)
		}
	}
	response := call("POST", "/api/autopilot/summary/ack", `{"boundary":0,"project_id":"one-project"}`)
	if response.Code != 400 {
		t.Fatal(response.Code, response.Body.String())
	}
}
