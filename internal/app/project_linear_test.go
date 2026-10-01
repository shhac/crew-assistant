package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

func TestProjectLinearLargeDescriptionsAndKnownIssues(t *testing.T) {
	a, _ := readyLinearProject(t)
	reads := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		if args[2] == connections.LinearIssueContextQuery {
			reads++
			var vars struct {
				ID string `json:"id"`
			}
			json.Unmarshal([]byte(args[4]), &vars)
			description := strings.Repeat("A realistic specification. ", 2000)
			if vars.ID == "0" {
				description = strings.Repeat("x", 150*1024)
			}
			data, _ := json.Marshal(map[string]any{"issue": map[string]any{"id": vars.ID, "description": description}})
			return data, nil
		}
		if strings.Contains(args[2], "description") || !strings.Contains(args[2], "first: 10") {
			t.Fatal("unbounded metadata query")
		}
		nodes := []core.LinearIssue{}
		for n := 0; n < 10; n++ {
			id := fmt.Sprint(n)
			nodes = append(nodes, core.LinearIssue{ID: id, Identifier: "EX-" + id, Title: "Work", URL: "https://linear.app/issue/" + id})
		}
		data, _ := json.Marshal(map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}})
		return data, nil
	}}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	if len(snap.Tasks) != 10 {
		t.Fatal("large descriptions blocked imports", len(snap.Tasks))
	}
	for _, task := range snap.Tasks {
		d := task.Linear[0].Description
		if len(d) > connections.LinearDescriptionLimit || !strings.Contains(d, "Description ") {
			t.Fatal("description not bounded and marked", len(d))
		}
	}
	if !strings.Contains(sinceLastTurn(snap, time.Now().Add(-time.Hour)), "EX-") {
		t.Fatal("assistant cannot see imported tasks")
	}
	if err := a.SyncLinear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads != 10 {
		t.Fatal("known issues fetched again", reads)
	}
}

func TestProjectLinearContextFailureImportsNothing(t *testing.T) {
	a, _ := readyLinearProject(t)
	reads := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		if args[2] == connections.LinearIssueContextQuery {
			reads++
			if reads == 2 {
				return nil, errors.New("unavailable")
			}
			return []byte(`{"issue":{"id":"one","description":"Readable"}}`), nil
		}
		return []byte(`{"issues":{"nodes":[{"id":"one","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/EX-1"},{"id":"two","identifier":"EX-2","title":"Work","url":"https://linear.app/issue/EX-2"}],"pageInfo":{"hasNextPage":false}}}`), nil
	}}
	if err := a.SyncLinear(context.Background()); err == nil {
		t.Fatal("context failure ignored")
	}
	snap, _ := a.Core.Snapshot(context.Background())
	if len(snap.Tasks) != 0 || len(snap.Projects[0].LinearImported) != 0 || snap.Projects[0].Linear.LastError == "" {
		t.Fatal("partial import or missing error")
	}
}

func TestProjectLinearSweepIsolationAndNoDuplicates(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var linked []core.Project
	for _, name := range []string{"Bad", "Good", "Unlinked"} {
		p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: name, Template: "draft", Brief: core.BriefInput{Goal: "Work"}})
		if err != nil {
			t.Fatal(err)
		}
		if name != "Unlinked" {
			l := core.LinearLink{ConnectionID: "lin", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: name, Rules: core.LinearRules{PickUp: true, Assignee: "any"}}
			if name == "Bad" {
				l.ID = "22222222-2222-2222-2222-222222222222"
			}
			p, err = a.Core.SetProjectLinear(ctx, p.ID, &l)
			if err != nil {
				t.Fatal(err)
			}
			linked = append(linked, p)
		}
	}
	bad := true
	calls := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		calls++
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		if data, ok := fakeLinearContext(args); ok {
			return data, nil
		}
		var vars map[string]json.RawMessage
		_ = json.Unmarshal([]byte(args[4]), &vars)
		if bad && string(vars["filter"]) == `{"team":{"id":{"eq":"22222222-2222-2222-2222-222222222222"}}}` {
			return nil, errors.New("failure")
		}
		return []byte(`{"issues":{"nodes":[{"id":"one","identifier":"EX-1","title":"Work","url":"https://linear.app/example/issue/EX-1"}],"pageInfo":{"hasNextPage":false}}}`), nil
	}}
	if err := a.SyncLinear(ctx); err == nil {
		t.Fatal("failure hidden")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Tasks) != 1 || snap.Tasks[0].ProjectID != linked[1].ID || snap.Projects[0].Linear.LastError == "" {
		t.Fatalf("isolation: %+v", snap)
	}
	bad = false
	if err := a.SyncLinear(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncLinear(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if len(snap.Tasks) != 2 || snap.Projects[0].Linear.LastError != "" || snap.Projects[2].Linear != nil {
		t.Fatal("duplicates or error not cleared", snap)
	}
	a.Demo = true
	before := calls
	if err := a.SyncLinear(ctx); err != nil || calls != before {
		t.Fatal("demo read", err)
	}
}

func readyLinearProject(t *testing.T) (*App, core.Project) {
	t.Helper()
	a := testApp(t)
	ctx := context.Background()
	cfg := a.Config()
	cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Work", Template: "draft", Brief: core.BriefInput{Goal: "Work"}})
	if err != nil {
		t.Fatal(err)
	}
	l := core.LinearLink{ConnectionID: "lin", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Team", Rules: core.LinearRules{PickUp: true, Assignee: "any"}}
	p, err = a.Core.SetProjectLinear(ctx, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestProjectLinearSweepRemovedConnectionOrProfile(t *testing.T) {
	for _, remove := range []string{"connection", "profile"} {
		t.Run(remove, func(t *testing.T) {
			a, p := readyLinearProject(t)
			cfg := a.Config()
			if remove == "connection" {
				cfg.Connections = nil
			} else {
				cfg.Connections[0].Profiles = []string{"other"}
			}
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			a.connectionClient = connections.Client{Run: func(context.Context, string, []string) ([]byte, error) {
				t.Fatal("removed scope ran a CLI")
				return nil, nil
			}}
			if err := a.SyncLinear(context.Background()); err == nil {
				t.Fatal("removal hidden")
			}
			snap, _ := a.Core.Snapshot(context.Background())
			if len(snap.Tasks) != 0 || len(snap.Projects[0].LinearImported) != 0 || snap.Projects[0].Linear == nil {
				t.Fatal(snap)
			}
			expected := "the Linear connection this project used is gone"
			if snap.Projects[0].ID != p.ID || snap.Projects[0].Linear.LastError != expected {
				t.Fatal("missing error", snap.Projects[0])
			}
		})
	}
}

func TestProjectLinearSweepUnreadyProjects(t *testing.T) {
	for _, missing := range []string{"brief", "playbook"} {
		t.Run(missing, func(t *testing.T) {
			a, p := readyLinearProject(t)
			ctx := context.Background()
			snap, _ := a.Core.Snapshot(ctx)
			l := *snap.Projects[0].Linear
			off := l
			off.Rules.PickUp = false
			if _, err := a.Core.SetProjectLinear(ctx, p.ID, &off); err != nil {
				t.Fatal(err)
			}
			in := core.ProjectInput{Title: "Unready", Template: "draft", Brief: core.BriefInput{Goal: "Work"}}
			if missing == "brief" {
				in.Brief.Goal = ""
			} else {
				in.Template = ""
			}
			var err error
			p, err = a.Core.CreateProject(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			p, err = a.Core.SetProjectLinear(ctx, p.ID, &l)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.Core.SetProjectLinearCursor(ctx, p.ID, p.Linear.Version, "saved"); err != nil {
				t.Fatal(err)
			}
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				t.Fatal("unready project made an external read")
				return nil, errors.New("unexpected read")
			}}
			if err := a.SyncLinear(ctx); err == nil {
				t.Fatal("unready project accepted")
			}
			snap, _ = a.Core.Snapshot(ctx)
			if len(snap.Tasks) != 0 {
				t.Fatal("created task", snap.Tasks)
			}
			for _, project := range snap.Projects {
				if project.ID == p.ID {
					if project.Linear.Cursor != "saved" {
						t.Fatal("readiness failure lost scan progress")
					}
					if len(project.LinearImported) != 0 || project.Linear.LastError == "" {
						t.Fatal("missing readiness error or marked imported", project)
					}
					if missing == "brief" && !strings.Contains(project.Linear.LastError, "give the project a brief") {
						t.Fatal(project.Linear.LastError)
					}
				}
			}
		})
	}
}

func TestProjectLinearSweepContinuesLargeBacklogWithoutReceiptVariables(t *testing.T) {
	a, _ := readyLinearProject(t)
	ctx := context.Background()
	const total = 751
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		if data, ok := fakeLinearContext(args); ok {
			return data, nil
		}
		var vars struct {
			After  string         `json:"after"`
			Filter map[string]any `json:"filter"`
		}
		if err := json.Unmarshal([]byte(args[4]), &vars); err != nil {
			t.Fatal(err)
		}
		if _, ok := vars.Filter["id"]; ok || len(args[4]) > 512 {
			t.Fatal("unbounded receipt list sent", args[4])
		}
		start := 0
		if vars.After != "" {
			if _, err := fmt.Sscan(vars.After, &start); err != nil {
				t.Fatal(err)
			}
		}
		end := min(start+10, total)
		nodes := []core.LinearIssue{}
		for n := start; n < end; n++ {
			id := fmt.Sprint(n)
			nodes = append(nodes, core.LinearIssue{ID: id, Identifier: "EX-" + id, Title: "Work", URL: "https://linear.app/example/issue/" + id, Description: "Keep column names."})
		}
		data, _ := json.Marshal(map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": end < total, "endCursor": fmt.Sprint(end)}}})
		return data, nil
	}}
	for range 19 {
		if err := a.SyncLinear(ctx); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	if len(snap.Tasks) != total || len(snap.Projects[0].LinearImported) != total || snap.Projects[0].Linear.Cursor != "" {
		t.Fatal("backlog stalled", len(snap.Tasks), snap.Projects[0].Linear.Cursor)
	}
	if err := a.SyncLinear(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if len(snap.Tasks) != total {
		t.Fatal("reimported", len(snap.Tasks))
	}
	if snap.Tasks[0].Linear[0].Description != "Keep column names." || len(snap.Tasks[0].Criteria) != 0 {
		t.Fatal("description lost or URL is criteria")
	}
}
func TestProjectLinearSweepReadFailureAndCaps(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			cfg := a.Config()
			cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
			_ = a.UpdateConfig(cfg)
			p, _ := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Work", Template: "draft", Brief: core.BriefInput{Goal: "Work"}})
			l := core.LinearLink{ConnectionID: "lin", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Team", Rules: core.LinearRules{PickUp: true, Assignee: "any"}}
			_, err := a.Core.SetProjectLinear(ctx, p.ID, &l)
			if err != nil {
				t.Fatal(err)
			}
			pages := 0
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[0] == "auth" {
					return []byte(`{"alias":"home"}`), nil
				}
				if data, ok := fakeLinearContext(args); ok {
					return data, nil
				}
				pages++
				if fail && pages == 2 {
					return nil, errors.New("partial read")
				}
				nodes := []core.LinearIssue{}
				for i := 0; i < 10; i++ {
					id := fmt.Sprintf("%d-%d", pages, i)
					nodes = append(nodes, core.LinearIssue{ID: id, Identifier: "EX-" + id, Title: "Work", URL: "https://linear.app/example/issue/" + id})
				}
				data, _ := json.Marshal(map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": fmt.Sprint(pages)}}})
				return data, nil
			}}
			err = a.SyncLinear(ctx)
			snap, _ := a.Core.Snapshot(ctx)
			if fail {
				if err == nil || len(snap.Tasks) != 0 {
					t.Fatal("partial failure imported", err, len(snap.Tasks))
				}
			} else {
				if err != nil || pages != 5 || len(snap.Tasks) != 50 {
					t.Fatal("caps", pages, len(snap.Tasks), err)
				}
				visible, err := a.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, st := range visible.Integrations {
					if st.ID == "linear-project:"+p.ID {
						found = strings.Contains(st.Detail, "more may be waiting")
					}
				}
				if !found {
					t.Fatal("backlog status is invisible")
				}
			}
		})
	}
}

// fakeLinearContext supplies a separate issue-context response for synthetic reads.
func fakeLinearContext(args []string) ([]byte, bool) {
	if len(args) < 5 || args[2] != connections.LinearIssueContextQuery {
		return nil, false
	}
	var vars struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(args[4]), &vars)
	data, _ := json.Marshal(map[string]any{"issue": map[string]any{"id": vars.ID, "description": "Keep column names."}})
	return data, true
}

func TestProjectLinearSavedCursorRecovery(t *testing.T) {
	a, p := readyLinearProject(t)
	ctx := context.Background()
	if err := a.Core.SetProjectLinearCursor(ctx, p.ID, p.Linear.Version, "deleted"); err != nil {
		t.Fatal(err)
	}
	auth := 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			auth++
			return []byte(`{"alias":"home"}`), nil
		}
		if data, ok := fakeLinearContext(args); ok {
			return data, nil
		}
		if strings.Contains(args[4], "deleted") {
			return nil, errors.New("invalid cursor")
		}
		return []byte(`{"issues":{"nodes":[{"id":"one","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/one"}],"pageInfo":{"hasNextPage":false}}}`), nil
	}}
	if err := a.SyncLinear(ctx); err == nil {
		t.Fatal("failed read hidden")
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Projects[0].Linear.Cursor != "" || len(snap.Tasks) != 0 {
		t.Fatal("cursor not reset safely")
	}
	if err := a.SyncLinear(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Snapshot(ctx)
	if len(snap.Tasks) != 1 || snap.Projects[0].Linear.LastError != "" || auth != 2 {
		t.Fatal("recovery or discovery", auth, snap.Projects[0])
	}
	found := false
	for _, st := range snap.Integrations {
		if st.ID == "linear-project:"+p.ID {
			found = st.ProjectID == p.ID && st.Detail == "1 issues picked up"
		}
	}
	if !found {
		t.Fatal("project status not surfaced")
	}
}

func TestProjectLinearFailuresKeepSavedProgress(t *testing.T) {
	for _, failure := range []string{"head-page", "later-page", "description", "discovery"} {
		t.Run(failure, func(t *testing.T) {
			a, p := readyLinearProject(t)
			ctx := context.Background()
			if err := a.Core.SetProjectLinearCursor(ctx, p.ID, p.Linear.Version, "saved"); err != nil {
				t.Fatal(err)
			}
			pages := 0
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[0] == "auth" {
					if failure == "discovery" {
						return nil, errors.New("signed out")
					}
					return []byte(`{"alias":"home"}`), nil
				}
				if args[2] == connections.LinearIssueContextQuery {
					return nil, errors.New("context unavailable")
				}
				pages++
				if failure == "head-page" {
					return nil, errors.New("top read unavailable")
				}
				if strings.Contains(args[4], `"after":null`) {
					return []byte(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`), nil
				}
				if failure == "later-page" {
					if pages == 3 {
						return nil, errors.New("temporary read failure")
					}
					if !strings.Contains(args[4], `"after":"saved"`) {
						t.Fatal("lost saved progress", args[4])
					}
					return []byte(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"later"}}}`), nil
				}
				return []byte(`{"issues":{"nodes":[{"id":"one","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/one"}],"pageInfo":{"hasNextPage":false}}}`), nil
			}}
			if err := a.SyncLinear(ctx); err == nil {
				t.Fatal("read failure hidden")
			}
			snap, _ := a.Core.Snapshot(ctx)
			if snap.Projects[0].Linear.Cursor != "saved" || snap.Projects[0].Linear.LastError == "" || len(snap.Tasks) != 0 {
				t.Fatal("failed sweep discarded progress", snap.Projects[0].Linear)
			}
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[0] == "auth" {
					return []byte(`{"alias":"home"}`), nil
				}
				if !strings.Contains(args[4], `"after":"saved"`) && !strings.Contains(args[4], `"after":null`) {
					t.Fatal("retry lost its head read or saved progress", args[4])
				}
				return []byte(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`), nil
			}}
			if err := a.SyncLinear(ctx); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			if snap.Projects[0].Linear.LastError != "" {
				t.Fatal("success did not clear error")
			}
		})
	}
}

func TestProjectLinearSettingsChangedDuringSweep(t *testing.T) {
	for _, unlink := range []bool{false, true} {
		t.Run(fmt.Sprint(unlink), func(t *testing.T) {
			a, p := readyLinearProject(t)
			ctx := context.Background()
			a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
				if args[0] == "auth" {
					return []byte(`{"alias":"home"}`), nil
				}
				if data, ok := fakeLinearContext(args); ok {
					var err error
					if unlink {
						_, err = a.Core.ClearProjectLinear(ctx, p.ID)
					} else {
						_, err = a.Core.SetProjectLinear(ctx, p.ID, p.Linear)
					}
					if err != nil {
						t.Fatal(err)
					}
					return data, nil
				}
				return []byte(`{"issues":{"nodes":[{"id":"one","identifier":"EX-1","title":"Work","url":"https://linear.app/issue/one"}],"pageInfo":{"hasNextPage":false}}}`), nil
			}}
			if err := a.SyncLinear(ctx); err != nil {
				t.Fatal("expected conflict became failure", err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			if len(snap.Tasks) != 0 {
				t.Fatal("stale import")
			}
			if st := a.statuses["linear-project:"+p.ID]; st.Status == "error" || st.Detail != "Settings changed; skipped" {
				t.Fatal(st)
			}
		})
	}
}

func TestProjectLinearResumedScanChecksNewestIssues(t *testing.T) {
	a, p := readyLinearProject(t)
	ctx := context.Background()
	known := core.LinearIssue{ID: "known", Identifier: "EX-known", Title: "Already imported", URL: "https://linear.app/issue/known"}
	if _, err := a.Core.PickUpLinearIssue(ctx, p.ID, p.Linear.Version, known); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.SetProjectLinearCursor(ctx, p.ID, p.Linear.Version, "2000"); err != nil {
		t.Fatal(err)
	}
	auth, pages, descriptions := 0, 0, 0
	a.connectionClient = connections.Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			auth++
			return []byte(`{"alias":"home"}`), nil
		}
		if data, ok := fakeLinearContext(args); ok {
			descriptions++
			if strings.Contains(args[4], "known") {
				t.Fatal("known description reread")
			}
			return data, nil
		}
		pages++
		var vars struct {
			After string `json:"after"`
		}
		if err := json.Unmarshal([]byte(args[4]), &vars); err != nil {
			t.Fatal(err)
		}
		nodes := []core.LinearIssue{}
		end := "top"
		if vars.After == "" {
			if pages != 1 {
				t.Fatal("top must be first")
			}
			nodes = append(nodes, core.LinearIssue{ID: "new", Identifier: "EX-new", Title: "New arrival", URL: "https://linear.app/issue/new"}, known)
		} else {
			start := 0
			if _, err := fmt.Sscan(vars.After, &start); err != nil {
				t.Fatal(err)
			}
			if start < 2000 {
				t.Fatal("head cursor replaced saved backlog position")
			}
			for n := start; n < start+10; n++ {
				id := fmt.Sprint(n)
				nodes = append(nodes, core.LinearIssue{ID: id, Identifier: "EX-" + id, Title: "Backlog", URL: "https://linear.app/issue/" + id})
			}
			end = fmt.Sprint(start + 10)
		}
		data, _ := json.Marshal(map[string]any{"issues": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": end}}})
		return data, nil
	}}
	if err := a.SyncLinear(ctx); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	newest := 0
	for _, task := range snap.Tasks {
		if task.Linear[0].ID == "new" {
			newest++
		}
	}
	if newest != 1 || len(snap.Tasks) != 42 || pages != 5 || auth != 1 || descriptions != 41 || snap.Projects[0].Linear.Cursor != "2040" {
		t.Fatalf("newest=%d tasks=%d pages=%d auth=%d context=%d cursor=%s", newest, len(snap.Tasks), pages, auth, descriptions, snap.Projects[0].Linear.Cursor)
	}
}
