package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// standInDaemon serves a fixed state, and a place for any task, where the
// CLI looks for a running daemon; it records the task places asked for.
func standInDaemon(t *testing.T, snap core.Snapshot) (*options, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer stand-in-token" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(snap)
	})
	mux.HandleFunc("GET /api/projects/{project}/tasks/{task}/place", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.PathValue("project")+"/"+r.PathValue("task"))
		mu.Unlock()
		json.NewEncoder(w).Encode(work.TaskPlace{ProjectID: r.PathValue("project"), TaskID: r.PathValue("task"), Workspace: "/work/" + r.PathValue("task")})
	})
	o := standIn(t, mux, "stand-in-token")
	return o, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func sharedPrefixState() core.Snapshot {
	return core.Snapshot{
		Projects: []core.Project{{ID: "proj-1", Title: "Fictional project", Prefix: "CA"}},
		Tasks: []core.Task{
			{ID: "abc124", ProjectID: "proj-1", Number: 11, Objective: "Second"},
			{ID: "abc123", ProjectID: "proj-1", Number: 12, Objective: "First"},
			{ID: "abc", ProjectID: "proj-1", Number: 13, Objective: "Short"},
			{ID: "xyz9", ProjectID: "proj-1", Number: 14, Objective: "Other"},
		},
	}
}

func TestFindingATaskByItsId(t *testing.T) {
	o, _ := standInDaemon(t, sharedPrefixState())
	tests := []struct {
		name, ref, want string
	}{
		{name: "an exact id beats the ids it starts", ref: "abc", want: "abc"},
		{name: "a shared start is ambiguous", ref: "abc12", want: ""},
		{name: "a unique start resolves", ref: "xy", want: "xyz9"},
		{name: "a readable id", ref: "CA-12", want: "abc123"},
		{name: "a readable id in any case", ref: "ca-11", want: "abc124"},
		{name: "surrounding space is ignored", ref: "  abc123 ", want: "abc123"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := o.findTask(tc.ref)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("%q resolved to %s, want it refused as ambiguous", tc.ref, got.ID)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.ID != tc.want {
				t.Fatalf("%q resolved to %s, want %s", tc.ref, got.ID, tc.want)
			}
		})
	}
}

func TestAnAmbiguousTaskIdListsItsCandidates(t *testing.T) {
	o, _ := standInDaemon(t, sharedPrefixState())
	_, err := o.findTask("abc1")
	if err == nil {
		t.Fatal("a start shared by two ids was accepted")
	}
	if !strings.Contains(err.Error(), "abc123, abc124") {
		t.Fatalf("the refusal %q does not list the candidates in order", err)
	}
	if strings.Contains(err.Error(), "xyz9") || strings.Contains(err.Error(), `abc,`) {
		t.Fatalf("the refusal %q lists ids the start does not match", err)
	}
}

func TestAnUnknownTaskIsRefused(t *testing.T) {
	o, _ := standInDaemon(t, sharedPrefixState())
	for _, ref := range []string{"nope", "CA-99", ""} {
		if got, err := o.findTask(ref); err == nil {
			t.Fatalf("%q resolved to %s", ref, got.ID)
		}
	}
}

func TestPlacingATaskAsksTheDaemonForTheResolvedTask(t *testing.T) {
	o, asked := standInDaemon(t, sharedPrefixState())
	place, task, err := o.place("CA-12")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "abc123" || place.TaskID != "abc123" || place.Workspace != "/work/abc123" {
		t.Fatalf("placed %s at %+v, want abc123", task.ID, place)
	}
	if got := asked(); len(got) != 1 || got[0] != "proj-1/abc123" {
		t.Fatalf("the daemon was asked for %v, want proj-1/abc123", got)
	}

	if _, _, err := o.place("abc1"); err == nil {
		t.Fatal("an ambiguous id was placed")
	}
	if got := asked(); len(got) != 1 {
		t.Fatalf("an ambiguous id still asked the daemon: %v", got)
	}
}
