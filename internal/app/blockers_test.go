package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func blockerCall(t *testing.T, a *App, name string, in any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return a.Execute(context.Background(), name, raw)
}

func blockerFixture(t *testing.T, git bool) (*App, core.Project, core.Task, core.Task) {
	t.Helper()
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Write notes"}})
	if err != nil {
		t.Fatal(err)
	}
	if git {
		pb := *p.Playbook
		pb.Medium, pb.Repo, pb.BranchPrefix = core.MediumGit, t.TempDir(), "crew/"
		p, err = a.Core.SetPlaybook(ctx, p.ID, pb)
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	return a, p, first, second
}

func TestAssistantBlockersHoldAgainstTheTeam(t *testing.T) {
	for _, holds := range []string{"", "start", "landing"} {
		t.Run("holds="+holds, func(t *testing.T) {
			a, p, task, other := blockerFixture(t, false)
			out, err := blockerCall(t, a, "set_blocker", map[string]string{"project_id": p.ID, "task_id": task.Ref, "kind": "manual", "description": "Owner is ready", "holds": holds})
			if err != nil {
				t.Fatal(err)
			}
			blocked := out.(core.Task)
			if len(blocked.Blockers) != 1 {
				t.Fatalf("blockers: %+v", blocked.Blockers)
			}
			b := blocked.Blockers[0]
			if b.By != core.LinkedByAssistant || b.ClearedAt != nil || b.LandingOnly != (holds == "landing") {
				t.Fatalf("blocker: %+v", b)
			}
			if _, err = a.Work.ClearBlocker(context.Background(), p.ID, task.ID, b.ID, core.LinkedByPM, "ready"); !errors.Is(err, core.ErrConflict) {
				t.Fatalf("PM clear: %v", err)
			}
			snap, err := a.Core.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			current, _ := snap.FindTask(task.ID)
			if current.Blockers[0].ClearedAt != nil || len(core.BlockerReasons(current)) != 1 {
				t.Fatal("team removed the hold")
			}
			next, ok, err := a.Core.NextTask(context.Background())
			want := other.ID
			if holds == "landing" {
				want = task.ID
			}
			if err != nil || !ok || next.ID != want {
				t.Fatalf("next: %+v %v %v", next, ok, err)
			}
		})
	}
}

func TestAssistantDaemonBlockersAndInvalidArguments(t *testing.T) {
	a, p, task, target := blockerFixture(t, true)
	out, err := blockerCall(t, a, "set_blocker", map[string]string{"project_id": p.ID, "task_id": task.Ref, "kind": "daemon_includes", "other_task_id": target.Ref})
	if err != nil {
		t.Fatal(err)
	}
	b := out.(core.Task).Blockers[0]
	if b.Task != target.ID || b.Description != "the running daemon includes "+target.Ref || b.By != core.LinkedByAssistant {
		t.Fatalf("blocker: %+v", b)
	}
	docs, dp, dt, do := blockerFixture(t, false)
	for _, tc := range []struct {
		name                                           string
		app                                            *App
		project, task, kind, description, other, holds string
		extra                                          bool
	}{
		{"non-git", docs, dp.ID, dt.Ref, "daemon_includes", "", do.Ref, "", false},
		{"self", a, p.ID, task.Ref, "daemon_includes", "", task.Ref, "", false},
		{"missing target", a, p.ID, task.Ref, "daemon_includes", "", "missing", "", false},
		{"cross-project", a, p.ID, task.Ref, "daemon_includes", "", "", "", false},
		{"missing task", a, p.ID, "missing", "manual", "Ready", "", "", false},
		{"kind", a, p.ID, task.Ref, "unknown", "Ready", "", "", false},
		{"description", a, p.ID, task.Ref, "manual", "", "", "", false},
		{"holds", a, p.ID, task.Ref, "manual", "Ready", "", "forever", false},
		{"unknown field", a, p.ID, task.Ref, "manual", "Ready", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "cross-project" {
				foreign, err := a.Core.CreateProject(context.Background(), core.ProjectInput{Title: "Other", Template: "draft", Brief: core.BriefInput{Goal: "Other work"}})
				if err != nil {
					t.Fatal(err)
				}
				ft, err := a.Core.QueueTask(context.Background(), foreign.ID, core.TaskInput{Objective: "Foreign"})
				if err != nil {
					t.Fatal(err)
				}
				tc.other = ft.ID
			}
			before, err := tc.app.Core.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			in := map[string]string{"project_id": tc.project, "task_id": tc.task, "kind": tc.kind, "description": tc.description, "other_task_id": tc.other, "holds": tc.holds}
			if tc.extra {
				in["unexpected"] = "value"
			}
			if _, err := blockerCall(t, tc.app, "set_blocker", in); err == nil {
				t.Fatal("accepted invalid arguments")
			}
			after, err := tc.app.Core.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid call wrote state")
			}
		})
	}
}

func TestAssistantClearsAnyBlockerAndRecordsAuthor(t *testing.T) {
	a, p, task, _ := blockerFixture(t, false)
	for _, by := range []string{core.LinkedByOwner, core.LinkedByPM, core.LinkedByAssistant} {
		blocked, err := a.Work.SetBlocker(context.Background(), core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "Ready " + by, By: by})
		if err != nil {
			t.Fatal(err)
		}
		b := blocked.Blockers[len(blocked.Blockers)-1]
		in := map[string]string{"project_id": p.ID, "task_id": task.Ref, "blocker_id": b.ID}
		out, err := blockerCall(t, a, "clear_blocker", in)
		if err != nil {
			t.Fatal(err)
		}
		cleared := out.(core.Task).Blockers[len(blocked.Blockers)-1]
		if cleared.ClearedAt == nil || cleared.ClearedBy != core.LinkedByAssistant {
			t.Fatalf("cleared: %+v", cleared)
		}
		snap, err := a.Core.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, e := range snap.Activity {
			if e.Kind == "task.unblocked" && e.Summary == b.Description+": cleared by the assistant" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("clearing activity count: %d", count)
		}
		if _, err := blockerCall(t, a, "clear_blocker", in); !errors.Is(err, core.ErrConflict) {
			t.Fatalf("repeat clear: %v", err)
		}
	}
	if _, err := blockerCall(t, a, "clear_blocker", map[string]string{"project_id": p.ID, "task_id": task.Ref, "blocker_id": "missing"}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown clear: %v", err)
	}
}

func TestAssistantOverviewShowsOnlyOpenBlockersAlongsideDependencies(t *testing.T) {
	a, p, task, target := blockerFixture(t, true)
	ctx := context.Background()
	if _, err := a.Work.LinkTasks(ctx, core.Link{Project: p.ID, Task: task.ID, Other: target.ID, Relation: "depends_on", By: core.LinkedByAssistant}); err != nil {
		t.Fatal(err)
	}
	blocked, err := a.Work.SetBlocker(ctx, core.BlockerInput{Project: p.ID, Task: task.ID, Kind: core.BlockerManual, Description: "Old condition", By: core.LinkedByOwner})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Work.ClearBlocker(ctx, p.ID, task.ID, blocked.Blockers[0].ID, core.LinkedByOwner, "ready"); err != nil {
		t.Fatal(err)
	}
	out, err := blockerCall(t, a, "set_blocker", map[string]string{"project_id": p.ID, "task_id": task.Ref, "kind": "daemon_includes", "other_task_id": target.Ref, "holds": "landing"})
	if err != nil {
		t.Fatal(err)
	}
	open := out.(core.Task).Blockers[1]
	if err = a.Core.CheckBlocker(ctx, task.ID, open.ID, "build has no commit"); err != nil {
		t.Fatal(err)
	}
	open.Check = "build has no commit"
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := func(view core.Snapshot) {
		t.Helper()
		got, ok := view.FindTask(task.ID)
		if !ok || len(got.WaitsFor) != 1 || got.WaitsFor[0] != target.Objective || len(got.Blockers) != 1 || !reflect.DeepEqual(got.Blockers[0], open) {
			t.Fatalf("overview: %+v", got)
		}
	}
	check(assistantView(snap))
	state, err := blockerCall(t, a, "read_state", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State core.Snapshot `json:"state"`
	}
	if err = json.Unmarshal(state.(json.RawMessage), &decoded); err != nil {
		t.Fatal(err)
	}
	check(decoded.State)
	detail, err := blockerCall(t, a, "read_task", map[string]string{"project_id": p.ID, "task_id": task.Ref})
	if err != nil {
		t.Fatal(err)
	}
	got := detail.(core.Task)
	if len(got.Blockers) != 2 || got.Blockers[0].ClearedAt == nil {
		t.Fatalf("history: %+v", got.Blockers)
	}
	raw, _ := json.Marshal(decoded.State.Tasks)
	if strings.Contains(string(raw), "Old condition") {
		t.Fatal("cleared history leaked into overview")
	}
}
