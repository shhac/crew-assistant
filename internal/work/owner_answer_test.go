package work

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/shhac/crew-assistant/internal/core"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOwnerAnswerMultilineReplacementRoundTrip(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			a, p, _ := loopApp(t, &scriptedRunner{}, "")
			multiline := strings.Repeat("x", 700) + "\n--dry-run must not write files\n•literal bullet"
			owner := "After landing, I will review it visually."
			task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Build", Criteria: []string{multiline, owner}})
			if err != nil {
				t.Fatal(err)
			}
			tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
			if kind == core.RolePM {
				tools = a.managerTools(p.ID, core.Role{Name: "PM"})
			}
			read := callTool(t, tools, "read_task", map[string]string{"task_id": task.ID})
			var criteria []string
			for _, line := range strings.Split(read.Content, "\n") {
				if raw, ok := strings.CutPrefix(line, "Requirements JSON: "); ok {
					if err := json.Unmarshal([]byte(raw), &criteria); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !slices.Equal(criteria, task.Criteria) {
				t.Fatalf("lossy read: %q", criteria)
			}
			raw, err := json.Marshal([]string{criteria[0], "Add regression tests"})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := a.Core.Snapshot(ctx)
			for _, bad := range []string{strings.Join(task.Criteria, "\n"), `["bad", 2]`} {
				if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "requirements": bad, "owner_checks": owner, "text_version": "0"}); !result.IsError {
					t.Fatal("accepted lossy/malformed replacement")
				}
			}
			after, _ := a.Core.Snapshot(ctx)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("invalid replacement changed record")
			}
			result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "requirements": string(raw), "owner_checks": owner, "text_version": "0"})
			if result.IsError {
				t.Fatal(result.Content)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := findTask(snap, p.ID, task.ID)
			if !slices.Equal(got.Criteria, []string{multiline, "Add regression tests"}) || !slices.Equal(got.OwnerChecks, []string{owner}) {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestOwnerAnswerUndoRestoresSameDraftEscalation(t *testing.T) {
	ctx := context.Background()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	owner := "After landing, I will review it visually."
	unrelated := core.Unreachable{Criterion: "Unrelated pending", Revision: 2, Why: "Keep this evidence"}
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDeciding
		t.Roles = nil
		t.Revisions = []core.Revision{{N: 1, BriefVersion: p.Brief.Version}}
		t.Criteria = []string{owner, "Team"}
		t.Unreachable = []core.Unreachable{{Criterion: owner, Revision: 1, Why: "Visual review"}, unrelated}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := a.managerTools(p.ID, core.Role{Name: "PM"})
	if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "owner_checks": owner}); result.IsError {
		t.Fatal(result.Content)
	}
	snap, _ := a.Core.Snapshot(ctx)
	folded, _ := findTask(snap, p.ID, task.ID)
	undone, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, folded.Edits[len(folded.Edits)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(undone.Unreachable, unrelated) {
		t.Fatal("undo lost unrelated pending work")
	}
	if err := a.decide(ctx, p, undone); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	got, _ := findTask(snap, p.ID, task.ID)
	d := openDecision(t, a, got)
	if d.OwnerStep == nil || d.OwnerStep.Criterion != owner {
		t.Fatalf("restored inability bypassed: %+v", d)
	}
}

func TestOwnerAnswerToolsReplayExistingMultilineCheck(t *testing.T) {
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	existing := strings.Repeat("x", 700) + "\nOwner review after landing"
	_, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.OwnerChecks = []string{existing}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := a.managerTools(p.ID, core.Role{Name: "PM"})
	read := callTool(t, tools, "read_task", map[string]string{"task_id": task.ID})
	var checks string
	for _, line := range strings.Split(read.Content, "\n") {
		if raw, ok := strings.CutPrefix(line, "Owner checks JSON: "); ok {
			checks = raw
		}
	}
	if checks == "" {
		t.Fatal("missing lossless check representation")
	}
	for _, add := range []string{"", "Team addition", "Team addition"} {
		if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "owner_checks": checks, "add_requirement": add}); result.IsError {
			t.Fatal(result.Content)
		}
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := findTask(snap, p.ID, task.ID)
	if !slices.Equal(got.OwnerChecks, []string{existing}) || len(got.Edits) != 1 || !slices.Contains(got.Criteria, "Team addition") {
		t.Fatalf("%+v", got)
	}
}

// Build a replacement from the tool's actual output, just as a role must,
// rather than preserving the fixture's criteria through a side channel.
func requirementsFromRead(t *testing.T, tools roleTools, id string) ([]string, string) {
	t.Helper()
	result := callTool(t, tools, "read_task", map[string]string{"task_id": id})
	if result.IsError {
		t.Fatal(result.Content)
	}
	var criteria []string
	var version string
	for _, line := range strings.Split(result.Content, "\n") {
		if criterion, ok := strings.CutPrefix(line, "- criterion: "); ok {
			criteria = append(criteria, criterion)
		}
		if n, ok := strings.CutPrefix(line, "Text version: "); ok {
			version = n
		}
	}
	if version == "" {
		t.Fatal("read_task omitted version")
	}
	return criteria, version
}

func TestOwnerAnswerReplacementPreservesLongCriteriaAfterReread(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		t.Run(kind, func(t *testing.T) {
			a, p, _ := loopApp(t, &scriptedRunner{}, "")
			long := strings.Repeat("界", 240) + " preserve this ending"
			task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Build", Criteria: []string{long, "--dry-run must not write files", "*literal star", "•literal bullet"}})
			if err != nil {
				t.Fatal(err)
			}
			tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
			if kind == core.RolePM {
				tools = a.managerTools(p.ID, core.Role{Name: "PM"})
			}
			criteria, version := requirementsFromRead(t, tools, task.ID)
			if !slices.Equal(criteria, []string{long, "--dry-run must not write files", "*literal star", "•literal bullet"}) {
				t.Fatalf("truncated read: %q", criteria)
			}
			// Another role appends work after the read. The stale mixed
			// replacement must fail, then preserve both criteria on reread.
			if _, err := a.Core.EditTask(context.Background(), core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RoleQA, Add: []string{"Concurrent requirement"}}); err != nil {
				t.Fatal(err)
			}
			clause := "after landing, I will review it visually."
			args := map[string]string{"task_id": task.ID, "requirements": strings.Join(append(criteria, "Add the regression test"), "\n"), "owner_checks": clause, "text_version": version}
			if result := callTool(t, tools, "edit_task", args); !result.IsError {
				t.Fatal("accepted stale replacement")
			}
			criteria, version = requirementsFromRead(t, tools, task.ID)
			args["requirements"] = strings.Join(append(criteria, "Add the regression test"), "\n")
			args["text_version"] = version
			if result := callTool(t, tools, "edit_task", args); result.IsError {
				t.Fatal(result.Content)
			}
			snap, err := a.Core.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got, _ := findTask(snap, p.ID, task.ID)
			if !slices.Equal(got.Criteria, []string{long, "--dry-run must not write files", "*literal star", "•literal bullet", "Concurrent requirement", "Add the regression test"}) || !slices.Equal(got.OwnerChecks, []string{clause}) || got.TextVersion != 2 || len(got.Edits) != 2 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestOwnerAnswerRetainedDirectionIsContext(t *testing.T) {
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	team := "Add the regression test"
	owner := "after landing, I will review it visually."
	answer := team + "; " + owner
	_, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Direction = append(t.Direction, answer)
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := a.managerTools(p.ID, core.Role{Name: "PM"})
	if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "add_requirement": team, "owner_checks": owner}); result.IsError {
		t.Fatal(result.Content)
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := findTask(snap, p.ID, task.ID)
	if !slices.Equal(got.Direction, []string{answer}) || !slices.Contains(got.Criteria, team) || slices.Contains(got.Criteria, owner) {
		t.Fatalf("%+v", got)
	}
	for name, prompt := range map[string]string{
		"implementer": writerPrompt(p, got, "", true),
		"reviewer":    reviewerPrompt(p, got, core.Revision{}),
		"QA":          checkerPrompt(p, got, core.Revision{}, core.Role{Kinds: []string{core.RoleQA}}, p.Playbook),
	} {
		if !strings.Contains(prompt, answer) || !strings.Contains(prompt, "Owner undertakings recorded in the after-landing checklist are context, not team requirements") {
			t.Fatalf("%s lost original answer or context clarification", name)
		}
		if strings.Contains(prompt, fmt.Sprintf("- %s\n", owner)) {
			t.Fatalf("%s assigned owner clause as criterion", name)
		}
	}
}

func TestOwnerAnswerSettlesPendingBeforeSameDraftDecision(t *testing.T) {
	for _, scenario := range []struct{ other, reword bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		other, reword := scenario.other, scenario.reword
		t.Run(fmt.Sprintf("unrelated pending %v reword %v", other, reword), func(t *testing.T) {
			ctx := context.Background()
			a, p, task := loopApp(t, &scriptedRunner{}, "")
			owned := "After landing, I will review it visually."
			remaining := "An unrelated live check"
			original := owned
			if reword {
				original = "Review it visually after landing"
			}
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Roles = nil
				t.Revisions = []core.Revision{{N: 1, BriefVersion: p.Brief.Version}}
				t.Criteria = append(t.Criteria, original)
				t.Unreachable = []core.Unreachable{{Criterion: original, Why: "Visual review", Revision: 1}}
				if other {
					t.Criteria = append(t.Criteria, remaining)
					t.Unreachable = append(t.Unreachable, core.Unreachable{Criterion: remaining, Why: "Another host", Revision: 1})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tools := a.managerTools(p.ID, core.Role{Name: "PM"})
			args := map[string]string{"task_id": task.ID, "owner_checks": owned}
			if reword {
				kept := slices.DeleteFunc(slices.Clone(task.Criteria), func(c string) bool { return c == original })
				args["requirements"] = strings.Join(kept, "\n")
				if len(kept) == 0 {
					args["requirements"] = clearAll
				}
				args["text_version"] = "0"
			}
			if result := callTool(t, tools, "edit_task", args); result.IsError {
				t.Fatal(result.Content)
			}
			snap, err := a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			task, _ = findTask(snap, p.ID, task.ID)
			if err := a.decide(ctx, p, task); err != nil {
				t.Fatal(err)
			}
			snap, err = a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			task, _ = findTask(snap, p.ID, task.ID)
			for _, pending := range task.Unreachable {
				if pending.Criterion == owned || pending.Criterion == original {
					t.Fatal("owned requirement remains pending")
				}
			}
			for _, decision := range snap.Decisions {
				if decision.OwnerStep != nil && (decision.OwnerStep.Criterion == owned || decision.OwnerStep.Criterion == original) {
					t.Fatal("owned requirement escalated again")
				}
			}
			if other {
				d := openDecision(t, a, task)
				if d.OwnerStep == nil || d.OwnerStep.Criterion != remaining || len(task.Unreachable) != 1 {
					t.Fatalf("lost unrelated pending work: %+v", task)
				}
			} else {
				if d := openDecision(t, a, task); d.Kind != core.DecisionDelivery || !strings.Contains(d.Context, owned) {
					t.Fatalf("delivery lost checklist: %+v", d)
				}
			}
		})
	}
}

func TestOwnerAnswerPMViewsPreserveCompleteCriteria(t *testing.T) {
	long := strings.Repeat("x", 380) + " preserve this ending"
	p := core.Project{ID: "p", Brief: core.Brief{Goal: "Build"}, Playbook: &core.Playbook{Medium: core.MediumGit, Land: core.LandPolicy{Target: "main"}}}
	task := core.Task{ID: "t", ProjectID: p.ID, Status: core.TaskTriage, Objective: "Build", Criteria: []string{long}}
	snap := core.Snapshot{Tasks: []core.Task{task}}
	for name, prompt := range map[string]string{
		"triage":  pmPrompt(snap, p),
		"landing": pmLandingPrompt(snap, p, task, core.Revision{}),
	} {
		if !strings.Contains(prompt, long) {
			t.Fatalf("%s shortened criterion", name)
		}
	}
}

func TestOwnerAnswerToolOnlyCheckAndReplacement(t *testing.T) {
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	tools := a.managerTools(p.ID, core.Role{Name: "PM"})
	clause := "After landing, I will review this."
	if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "owner_checks": clause}); result.IsError {
		t.Fatal(result.Content)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	got, _ := findTask(snap, p.ID, task.ID)
	if !slices.Equal(got.Criteria, task.Criteria) || !slices.Equal(got.OwnerChecks, []string{clause}) {
		t.Fatalf("%+v", got)
	}
	for _, args := range []map[string]string{
		{"task_id": task.ID, "owner_checks": "I will check live", "requirements": "Replacement"},
		{"task_id": task.ID, "owner_checks": "I will check live", "requirements": "Replacement", "text_version": "0"},
		{"task_id": task.ID, "owner_checks": "I will check live", "text_version": "invalid"},
		{"task_id": task.ID, "owner_checks": "   "},
	} {
		if result := callTool(t, tools, "edit_task", args); !result.IsError {
			t.Fatal("accepted malformed or stale edit")
		}
	}
	for _, kind := range []string{core.RoleImplementer, core.RoleReviewer, core.RoleQA} {
		role := a.toolsFor(task, kind, core.Role{})
		if result := callTool(t, role, "edit_task", map[string]string{"owner_checks": "I will check live", "add_requirement": "Sneaked"}); !result.IsError {
			t.Fatal("unauthorized owner edit")
		}
	}
	snap, _ = a.Core.Snapshot(context.Background())
	unchanged, _ := findTask(snap, p.ID, task.ID)
	if unchanged.TextVersion != got.TextVersion || len(unchanged.Edits) != len(got.Edits) || !slices.Equal(unchanged.Criteria, got.Criteria) || !slices.Equal(unchanged.OwnerChecks, got.OwnerChecks) {
		t.Fatal("refusal changed record")
	}
	criteria := strings.Join(append(slices.Clone(task.Criteria), "Regression"), "\n")
	if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "requirements": criteria, "owner_checks": "I will check live", "text_version": "1"}); result.IsError {
		t.Fatal(result.Content)
	}
	snap, _ = a.Core.Snapshot(context.Background())
	got, _ = findTask(snap, p.ID, task.ID)
	if got.TextVersion != 2 || len(got.OwnerChecks) != 2 || !slices.Contains(got.Criteria, "Regression") {
		t.Fatalf("%+v", got)
	}
}

func TestOwnerAnswerTools(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		for _, clause := range []string{"After landing, I will run this.", "I will check outside the sandbox.", "I will test on another host.", "I will do a live or visual review."} {
			t.Run(kind+clause, func(t *testing.T) {
				a, p, task := loopApp(t, &scriptedRunner{}, "")
				tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
				if kind == core.RolePM {
					tools = a.managerTools(p.ID, core.Role{Name: "PM"})
				}
				args := map[string]string{"task_id": task.ID, "owner_checks": clause, "add_requirement": "Add the regression test"}
				for i := 0; i < 2; i++ {
					if got := callTool(t, tools, "edit_task", args); got.IsError {
						t.Fatal(got.Content)
					}
				}
				snap, _ := a.Core.Snapshot(context.Background())
				got, _ := findTask(snap, p.ID, task.ID)
				if !slices.Equal(got.OwnerChecks, []string{clause}) || len(got.Edits) != 1 || !slices.Contains(got.Criteria, "Add the regression test") {
					t.Fatalf("%+v", got)
				}
				for _, c := range task.Criteria {
					if !slices.Contains(got.Criteria, c) {
						t.Fatal("lost criterion")
					}
				}
				if strings.Count(got.OwnerChecklist(), clause) != 1 {
					t.Fatal(got.OwnerChecklist())
				}
				if strings.Contains(briefText(p, got), clause) || !strings.Contains(briefText(p, got), "Add the regression test") {
					t.Fatal("team criteria contaminated")
				}
				for n, prompt := range []string{writerPrompt(p, got, "", true), reviewerPrompt(p, got, core.Revision{}), checkerPrompt(p, got, core.Revision{}, core.Role{Kinds: []string{core.RoleQA}}, p.Playbook)} {
					if strings.Contains(prompt, clause) || (n < 2 && !strings.Contains(prompt, "Add the regression test")) {
						t.Fatal("folded prompt criteria contaminated")
					}
				}
				if !strings.Contains(landedOn(&got, &p, core.Revision{}, "main", ""), got.OwnerChecklist()) {
					t.Fatal("delivery lost checklist")
				}
				if escalated, _ := parseOwnerSteps(`[{"requirement":"`+clause+`","why":"Live review"}]`, 1, got.Criteria, got.OwnersAlready(), got.TeamKept); len(escalated) > 0 {
					t.Fatal("owner check escalated")
				}
				read := callTool(t, tools, "read_task", map[string]string{"task_id": task.ID})
				if !strings.Contains(read.Content, clause) || !strings.Contains(read.Content, "Text version: 1") {
					t.Fatal(read.Content)
				}
			})
		}
	}
}

func TestOwnerAnswerAmbiguousAndTeamClausesRemainRequirements(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		a, p, task := loopApp(t, &scriptedRunner{}, "")
		tools := a.toolsFor(task, kind, core.Role{})
		if kind == core.RolePM {
			tools = a.managerTools(p.ID, core.Role{})
		}
		for _, clause := range []string{"Add regression tests", "Check outside the sandbox", "Test on another host", "Do a live check"} {
			if got := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "add_requirement": clause}); got.IsError {
				t.Fatal(got.Content)
			}
		}
		snap, _ := a.Core.Snapshot(context.Background())
		got, _ := findTask(snap, p.ID, task.ID)
		if len(got.OwnerChecks) != 0 || len(got.Criteria) != len(task.Criteria)+4 {
			t.Fatalf("%+v", got)
		}
		for _, phrase := range []string{"preserving the owner's wording", "Ambiguous answers retain", "one edit_task call", "never change brief criteria", "plan.owner_checks"} {
			if !strings.Contains(tools.guide(), phrase) {
				t.Fatalf("missing instruction %q", phrase)
			}
		}
	}
}
