package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDecisionsExportWritesRawJSONL(t *testing.T) {
	const body = "{\"decision_id\":\"one\"}\n{\"decision_id\":\"two\"}\n"
	o := standIn(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/decisions/evaluations.jsonl" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("bad request %v", r)
		}
		w.Write([]byte(body))
	}), "test-token")
	path := filepath.Join(t.TempDir(), "evaluations.jsonl")
	cmd := decisionsCommand(o)
	cmd.SetArgs([]string{"export", "--output", path})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != body {
		t.Fatalf("%s %v", got, err)
	}
}
