package work

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerAnswerOversizedCriteria(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		for _, transfer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/transfer=%v", kind, transfer), func(t *testing.T) {
				ctx := context.Background()
				a, p, task := loopApp(t, &scriptedRunner{}, "")
				criteria := make([]string, 41)
				for i := range criteria {
					criteria[i] = fmt.Sprintf("Unrelated criterion %d", i)
				}
				owner := "After landing, I will review it"
				initial := slices.Clone(criteria)
				if transfer {
					initial = append(initial, owner)
				}
				task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Criteria = initial; return "", nil })
				if err != nil {
					t.Fatal(err)
				}
				tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
				if kind == core.RolePM {
					tools = a.managerTools(p.ID, core.Role{Name: "PM"})
				}
				if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "owner_checks": owner}); result.IsError {
					t.Fatal(result.Content)
				}
				snap, _ := a.Core.Snapshot(ctx)
				got, _ := findTask(snap, p.ID, task.ID)
				if !slices.Equal(got.Criteria, criteria) || !slices.Equal(got.OwnerChecks, []string{owner}) {
					t.Fatalf("lost criteria: %+v", got)
				}
				if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "add_requirement": "New criterion"}); !result.IsError {
					t.Fatal("allowed oversized growth")
				}
			})
		}
	}
}

func TestOwnerAnswerDuringHandoff(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		for _, reword := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "running", true: "restart"}[resumed], map[bool]string{false: "exact", true: "reworded"}[reword]}, "/"), func(t *testing.T) {
				ctx := context.Background()
				a, p, task, m, h := handoffAt(t)
				owner := "After landing, I will review it visually."
				original := owner
				if reword {
					original = "Review it visually after landing"
				}
				other := "An unrelated live check"
				task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Criteria = append(t.Criteria, original, other)
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				// The implementer parses its report against the pre-fold snapshot.
				block, _ := json.Marshal([]map[string]string{{"requirement": original, "why": "Visual review"}, {"requirement": other, "why": "Another host"}})
				h.Unreachable, _ = parseOwnerSteps(string(block), h.Revision.N, task.Criteria, task.OwnersAlready(), task.TeamKept)
				if len(h.Unreachable) != 2 {
					t.Fatal(h.Unreachable)
				}
				if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					if resumed {
						t.Handoff = &h
					} else {
						t.Handoff = nil
					}
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
				args := map[string]string{"task_id": task.ID, "owner_checks": owner}
				if reword {
					kept := slices.DeleteFunc(slices.Clone(task.Criteria), func(c string) bool { return c == original })
					raw, _ := json.Marshal(kept)
					args["requirements"], args["text_version"] = string(raw), "0"
				}
				if result := callTool(t, a.managerTools(p.ID, core.Role{Name: "PM"}), "edit_task", args); result.IsError {
					t.Fatal(result.Content)
				}
				if resumed {
					a = restart(t, a)
				} else if err := a.handOff(ctx, task, m, h); err != nil {
					t.Fatal(err)
				}
				snap, err := a.Core.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got, _ := findTask(snap, p.ID, task.ID)
				if !slices.Equal(got.Unreachable, h.Unreachable[1:]) || got.Handoff != nil || len(got.Revisions) != 1 {
					t.Fatalf("handoff restored owner work or lost unrelated evidence: %+v", got)
				}
				got, err = a.Core.UpdateTask(ctx, got.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Status, t.Roles = core.TaskDeciding, nil
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.decide(ctx, p, got); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				got, _ = findTask(snap, p.ID, task.ID)

				// Late evidence must survive another daemon restart before undo.
				a = restart(t, a)
				undone, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, got.Edits[len(got.Edits)-1].ID)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(undone.Unreachable, h.Unreachable[0]) || !slices.Contains(undone.Unreachable, h.Unreachable[1]) {
					t.Fatalf("undo lost current-draft refusal: %+v", undone.Unreachable)
				}
				undone, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Status, t.DecisionID = core.TaskDeciding, ""
					// Put the restored refusal first for the decision, retaining unrelated work.
					t.Unreachable = append([]core.Unreachable{h.Unreachable[0]}, slices.DeleteFunc(slices.Clone(t.Unreachable), func(u core.Unreachable) bool { return u.Criterion == original })...)
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := a.decide(ctx, p, undone); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				restored, _ := findTask(snap, p.ID, task.ID)
				decision := openDecision(t, a, restored)
				if decision.OwnerStep == nil || decision.OwnerStep.Criterion != original {
					t.Fatalf("restored refusal bypassed: %+v", decision)
				}
				d := openDecision(t, a, got)
				if d.OwnerStep == nil || d.OwnerStep.Criterion != other {
					t.Fatalf("wrong pending escalation: %+v", d)
				}
			})
		}
	}
}

func TestOwnerAnswerBracketReplacement(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		t.Run(kind, func(t *testing.T) {
			a, p, task := loopApp(t, &scriptedRunner{}, "")
			criteria := []string{`["linux", "darwin"] are supported targets`, "[Linux] tests pass", "Documentation updated"}
			task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Criteria = slices.Clone(criteria)
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
			if kind == core.RolePM {
				tools = a.managerTools(p.ID, core.Role{Name: "PM"})
			}
			owner := "[Linux] After landing, I will check it."
			if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "requirements": strings.Join(criteria, "\n"), "owner_checks": owner, "text_version": "0"}); result.IsError {
				t.Fatal(result.Content)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			got, _ := findTask(snap, p.ID, task.ID)
			if !slices.Equal(got.Criteria, criteria) || !slices.Equal(got.OwnerChecks, []string{owner}) {
				t.Fatal(got.Criteria)
			}
		})
	}
}

func TestOwnerAnswerNewLiteralReplacement(t *testing.T) {
	for _, kind := range []string{core.RolePM, core.RoleResearcher} {
		for _, initial := range []string{"Team", `["linux"] are supported targets`} {
			t.Run(kind+"/"+initial, func(t *testing.T) {
				a, p, task := loopApp(t, &scriptedRunner{}, "")
				task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
					t.Criteria = []string{initial, "Documentation updated"}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
				tools := a.toolsFor(task, kind, core.Role{Name: "Planner"})
				if kind == core.RolePM {
					tools = a.managerTools(p.ID, core.Role{Name: "PM"})
				}
				want := []string{`["linux", "darwin"] are supported targets`, "Documentation updated"}
				owner := `["linux", "darwin"] After landing, I will review both.`
				if result := callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "requirements": strings.Join(want, "\n"), "owner_checks": owner, "text_version": "0"}); result.IsError {
					t.Fatal(result.Content)
				}
				snap, _ := a.Core.Snapshot(context.Background())
				got, _ := findTask(snap, p.ID, task.ID)
				if !slices.Equal(got.Criteria, want) || !slices.Equal(got.OwnerChecks, []string{owner}) {
					t.Fatalf("literal replacement: %+v", got)
				}
			})
		}
	}
}

func TestOwnerAnswerUndoOlderTransferEvidence(t *testing.T) {
	for _, scenario := range []string{"existing", "running", "restart", "redo-running", "redo-restart"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			a, p, task, m, h := handoffAt(t)
			criteria := []string{"After landing, I will check A", "After landing, I will check B"}
			pending := []core.Unreachable{{Criterion: criteria[0], Revision: h.Revision.N, Why: "Cannot check A"}, {Criterion: criteria[1], Revision: h.Revision.N, Why: "Cannot check B"}}
			_, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Criteria = slices.Clone(criteria)
				t.Handoff = nil
				if scenario == "existing" {
					t.Revisions = []core.Revision{h.Revision}
					t.Unreachable = slices.Clone(pending)
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			folded, err := a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RolePM, OwnerChecks: criteria[:1]})
			if err != nil {
				t.Fatal(err)
			}
			originalID := folded.Edits[len(folded.Edits)-1].ID
			if strings.HasPrefix(scenario, "redo-") {
				undone, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, originalID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, undone.Edits[len(undone.Edits)-1].ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RoleResearcher, OwnerChecks: criteria[1:]}); err != nil {
				t.Fatal(err)
			}
			if scenario != "existing" {
				h.Unreachable = slices.Clone(pending)
				if strings.HasSuffix(scenario, "restart") {
					if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Handoff = &h; return "", nil }); err != nil {
						t.Fatal(err)
					}
					a = restart(t, a)
				} else if err := a.handOff(ctx, task, m, h); err != nil {
					t.Fatal(err)
				}
			}
			// Retained handoff evidence must remain available across a further restart.
			a = restart(t, a)
			undone, err := a.Core.UndoTaskEdit(ctx, p.ID, task.ID, originalID)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(undone.Criteria, criteria) || len(undone.OwnerChecks) != 0 {
				t.Fatal("undo did not restore both requirements")
			}
			for _, u := range pending {
				if !slices.Contains(undone.Unreachable, u) {
					t.Fatalf("lost refusal: %+v", undone.Unreachable)
				}
			}
			// Exercise A first too: the redo's late evidence must protect the
			// original transfer, not just the independently transferred B.
			undone, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				slices.SortStableFunc(t.Unreachable, func(a, b core.Unreachable) int { return strings.Compare(a.Criterion, b.Criterion) })
				t.Status, t.Roles, t.DecisionID = core.TaskDeciding, nil, ""
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.decide(ctx, p, undone); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := findTask(snap, p.ID, task.ID)
			d := openDecision(t, a, got)
			if d.OwnerStep == nil || d.OwnerStep.Criterion != criteria[0] {
				t.Fatalf("original transfer refusal bypassed: %+v", d)
			}
			// Once A is settled, the actual deciding path must still escalate B.
			undone, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.SettleUnreachable(criteria[0])
				t.Status, t.Roles, t.DecisionID = core.TaskDeciding, nil, ""
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := a.decide(ctx, p, undone); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			got, _ = findTask(snap, p.ID, task.ID)
			d = openDecision(t, a, got)
			if d.OwnerStep == nil || d.OwnerStep.Criterion != criteria[1] {
				t.Fatalf("restored refusal bypassed: %+v", d)
			}
		})
	}
}

func TestOwnerAnswerHandoffKeepsCurrentAndUnrelatedEvidence(t *testing.T) {
	// Re-added criteria (including undo) and brief requirements still belong
	// to the team. Unknown report clauses retain their existing behavior.
	task := core.Task{
		Criteria:    []string{"Restored criterion", "Team part"},
		OwnerChecks: []string{"Owner check"}, OwnerSteps: []string{"Owner step"},
		OwnerTook: []string{"Original split", "Team part"},
		Edits:     []core.TaskEdit{{Before: core.TaskText{Criteria: []string{"Removed criterion", "Restored criterion", "Brief criterion"}}}},
	}
	p := core.Project{Brief: core.Brief{Criteria: []string{"Brief criterion"}}}
	var pending []core.Unreachable
	for _, c := range []string{"Owner check", "Owner step", "Original split", "Removed criterion", "Restored criterion", "Brief criterion", "Team part", "Unmatched report clause"} {
		pending = append(pending, core.Unreachable{Criterion: c, Revision: 1, Why: c})
	}
	applyHandoff(&task, &p, core.Handoff{Revision: core.Revision{N: 1}, Unreachable: pending})
	if !slices.Equal(task.Unreachable, pending[4:]) {
		t.Fatalf("lost current team or unrelated evidence: %+v", task.Unreachable)
	}
	if len(pending) != 8 || pending[0].Criterion != "Owner check" {
		t.Fatal("mutated prepared evidence")
	}
}
