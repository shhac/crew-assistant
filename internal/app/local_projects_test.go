package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

// Exercise the same coordination tools and supervision used by a live daemon:
// an unrelated configured account must not become a dependency of local work.
func TestLocalProjectLifecycleWithoutLinear(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "no_connections"
		if configured {
			name = "unrelated_work_account"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var starts, polls, connectorCalls atomic.Int32
			var finished atomic.Bool
			a, _ := runtimeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				run := worker.Run{ID: "local-run", Status: "running", Summary: "Working on the local project in isolation", UpdatedAt: time.Now().UTC()}
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/runs":
					starts.Add(1)
					var req worker.StartRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if req.ProjectID == "" || !strings.HasPrefix(req.Task, "Improve the local assistant\n\nDaemon project peer address book") {
						t.Errorf("unexpected commission: %+v", req)
					}
					run.DispatchKey = req.DispatchKey
				case r.Method == http.MethodGet && r.URL.Path == "/runs/local-run":
					polls.Add(1)
					if finished.Load() {
						run.Status = "completed"
						run.Summary = "Local acceptance checks passed"
						run.Evidence = []string{"Synthetic local acceptance report"}
					}
				default:
					t.Errorf("unexpected worker request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				writeRun(w, run)
			})
			cfg := a.Config()
			if configured {
				cfg.Connections = []config.Connection{{ID: "work-linear", Name: "Employer workspace", Tool: "lin", Profiles: []string{"work"}}}
			}
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			a.connectionClient = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) {
				connectorCalls.Add(1)
				return nil, errors.New("unrelated connection must not be used")
			}}
			execute := func(name string, value any) any {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				result, err := a.Execute(ctx, name, raw)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				return result
			}
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			project := execute("create_project", engine.CreateProjectArgs{Title: "Personal assistant project", Directories: []string{directory}}).(core.Project)
			execute("update_project", engine.UpdateProjectArgs{ProjectID: project.ID, Objective: "Improve the local assistant", AcceptanceCriteria: []string{"Local acceptance checks pass"}})
			execute("delegate", engine.DelegateArgs{ProjectID: project.ID, WorkerProfile: "fake", Role: "worker", Objective: "Improve the local assistant", AcceptanceCriteria: []string{"Local acceptance checks pass"}})
			if err := a.tick(ctx, false); err != nil {
				t.Fatal(err)
			}
			premature, _ := json.Marshal(engine.CompleteProjectArgs{ProjectID: project.ID, Evidence: []string{"Unverified claim"}})
			if _, err := a.Execute(ctx, "complete_project", premature); err == nil {
				t.Fatal("local project bypassed unfinished-work check")
			}
			finished.Store(true)
			if err := a.tick(ctx, false); err != nil {
				t.Fatal(err)
			}
			execute("complete_project", engine.CompleteProjectArgs{ProjectID: project.ID, Evidence: []string{"Reviewed synthetic local acceptance report"}})
			snap, err := a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.Projects) != 1 || snap.Projects[0].Status != "completed" || snap.Projects[0].SourceID != "" || len(snap.Projects[0].Directories) != 1 || snap.Projects[0].Directories[0] != directory {
				t.Fatalf("local project lost independence or completion: %+v", snap.Projects)
			}
			if len(snap.Agents) != 1 || snap.Agents[0].Status != "completed" || len(snap.Agents[0].Evidence) == 0 {
				t.Fatalf("missing worker evidence: %+v", snap.Agents)
			}
			if starts.Load() != 1 || polls.Load() != 1 || connectorCalls.Load() != 0 {
				t.Fatalf("starts=%d polls=%d external connector calls=%d", starts.Load(), polls.Load(), connectorCalls.Load())
			}
		})
	}
}
