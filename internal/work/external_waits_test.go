package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestCrossProjectLandingReplansOnceWithoutAnotherQuestion(t *testing.T) {
	t.Parallel()
	a, runner, p := plannedCode(t, 6)
	ctx := context.Background()
	other, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Library", Template: "draft", Brief: core.BriefInput{Goal: "Publish", Criteria: []string{"Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.SetProjectPaused(ctx, other.ID, true); err != nil {
		t.Fatal(err)
	}
	dep, err := a.Core.QueueTask(ctx, other.ID, core.TaskInput{Objective: "Publish library"})
	if err != nil {
		t.Fatal(err)
	}
	runner.plans = []string{
		fmt.Sprintf(`{"summary":"Implementation waits for %s to land", "depends_on":[%q]}`, dep.Ref, dep.ID),
		fmt.Sprintf(`{"summary":"Implementation waits for %s, which has now landed"}`, dep.Ref),
	}
	own, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use library"})
	if err != nil {
		t.Fatal(err)
	}
	held := taskNow(t, a, own.ID)
	if held.Status != core.TaskQueued || runner.edits != 0 {
		t.Fatalf("started before landing: %+v", held)
	}
	if _, err := a.Core.UpdateTask(ctx, dep.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskLanded; return "", nil }); err != nil {
		t.Fatal(err)
	}
	got := taskNow(t, a, own.ID)
	plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(plans) != 2 || runner.edits != 1 || got.Plan == nil || len(got.Plan.Questions) != 0 {
		t.Fatalf("replan turns %d edits %d task %+v", len(plans), runner.edits, got)
	}
	if strings.Contains(plans[1].Prompt, "This task already waits for") {
		t.Fatalf("finished wait left in prompt: %s", plans[1].Prompt)
	}
	if d := openDecision(t, a, got); d.Kind != core.DecisionDelivery {
		t.Fatalf("unexpected owner question: %+v", d)
	}
}

func TestResearchWaitSummaryIsSentBackOnce(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"still inconsistent", "declared prerequisite", "malformed retry", "malformed both"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			missing := `{"summary":"Implementation waits for lib-agent-harness v0.20.0 to be tagged"}`
			second := missing
			if outcome == "declared prerequisite" {
				second = `{"summary":"Implementation waits for lib-agent-harness v0.20.0 to be tagged", "prerequisites":["lib-agent-harness v0.20.0 tagged"]}`
			}
			if outcome == "malformed retry" {
				second = "I cannot provide JSON."
			}
			if outcome == "malformed both" {
				missing = "Implementation waits for the library to be tagged."
				second = missing
			}
			a, runner, p := plannedCode(t, 6, missing, second)
			queued, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Use library"})
			got := taskNow(t, a, queued.ID)
			var prompts []string
			for _, spec := range runner.seen {
				if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
					prompts = append(prompts, spec.Prompt)
				}
			}
			if len(prompts) != 2 {
				t.Fatalf("research prompts %v", prompts)
			}
			if outcome != "malformed both" && (!strings.Contains(prompts[1], "declares no depends_on or prerequisites") || !strings.Contains(prompts[1], "reword the summary if implementation does not actually wait")) {
				t.Fatalf("send-back omits the correction: %s", prompts[1])
			}
			if runner.edits != 0 || len(got.Revisions) != 0 {
				t.Fatal("implementation began")
			}
			snap, _ := a.Core.Snapshot(context.Background())
			if outcome == "declared prerequisite" {
				if got.Status != core.TaskQueued || len(got.Blockers) != 1 || len(snap.Decisions) != 1 || snap.Decisions[0].Kind != core.DecisionPrerequisite {
					t.Fatalf("held %+v decisions %+v", got, snap.Decisions)
				}
			} else {
				if got.Status != core.TaskWaiting || got.Plan == nil || !slices.Contains(got.Plan.Questions, "The plan says implementation waits for something but names nothing to wait for. Should it start?") {
					t.Fatalf("owner question %+v", got)
				}
			}
		})
	}
}

func TestParsePrerequisitesAndWaitSummaryGuard(t *testing.T) {
	t.Parallel()
	plan, _, _, err := parsePlan(`{"summary":"Use library", "prerequisites":[" lib tagged ","", "Owner ready"]}`, false, false)
	if err != nil || len(plan.Prerequisites) != 2 || plan.Prerequisites[0].What != "lib tagged" {
		t.Fatalf("parse %+v %v", plan, err)
	}
	for _, summary := range []string{"Implementation waits for a tag", "Work waits on a release", "Implementation must wait for X", "This task cannot start until X", "The work cannot start before X", "Implementation is blocked on X", "This task is blocked by X", "This task waits for X to land", "Implementation can't start until lib-agent-harness v0.20.0 is tagged", "Implementation can not start before X", "Implementation should wait for X", "Implementation will wait until X is ready", "Wait for X before implementing", "it waits for LIB-10 to land", "Waits for X to land", "Work waits on X to be tagged", "Work on this task will wait until the tag is ready", "Implementation can’t start until X"} {
		plan := core.Plan{Summary: summary}
		if !core.UndeclaredPlanWait(plan, nil, core.Task{}) {
			t.Errorf("missed %q", summary)
		}
		if core.UndeclaredPlanWait(plan, []string{"x"}, core.Task{}) || core.UndeclaredPlanWait(plan, nil, core.Task{WaitsFor: []string{"X"}}) || core.UndeclaredPlanWait(plan, nil, core.Task{Blockers: []core.Blocker{{Kind: core.BlockerPrerequisite}}}) {
			t.Errorf("existing wait rejected: %q", summary)
		}
	}
	for _, summary := range featureSummaries {
		if core.UndeclaredPlanWait(core.Plan{Summary: summary}, nil, core.Task{}) {
			t.Errorf("feature wording rejected: %q", summary)
		}
	}
	if core.UndeclaredPlanWait(core.Plan{Summary: "Implement the feature"}, nil, core.Task{}) || core.UndeclaredPlanWait(core.Plan{Summary: "Implementation waits", Prerequisites: []core.Prerequisite{{What: "release"}}}, nil, core.Task{}) {
		t.Fatal("unnecessary send-back")
	}
}

func TestOtherProjectsAreShownAndPrerequisiteOutcomesCarried(t *testing.T) {
	t.Parallel()
	own := core.Task{ID: "own", ProjectID: "a", Objective: "Use library"}
	same := core.Task{ID: "same", ProjectID: "a", Ref: "A-2", Objective: "Schema", Status: core.TaskQueued}
	other := core.Task{ID: "other", ProjectID: "b", Ref: "LIB-1", Objective: "Library tag", Status: core.TaskResearching}
	finished := core.Task{ID: "done", ProjectID: "b", Objective: "Done", Status: core.TaskLanded}
	projects := []core.Project{{ID: "a", Title: "App"}, {ID: "b", Title: "Library"}}
	tasks := otherWork(core.Snapshot{Projects: projects, Tasks: []core.Task{own, same, other, finished}}, own)
	if len(tasks) != 2 || tasks[1].ID != "other" {
		t.Fatal(tasks)
	}
	prompt := researcherPrompt(projects[0], own, tasks, projects)
	if !strings.Contains(prompt, "Unfinished tasks in your other projects:\nLibrary:\n- LIB-1") || strings.Contains(prompt, "Done") || !strings.Contains(prompt, `"prerequisites"`) {
		t.Fatal(prompt)
	}
	// A researcher once held a task back on tagging its own release, which
	// can only follow the task landing.
	if !strings.Contains(prompt, "What can only happen after this task lands") || !strings.Contains(prompt, "is not a prerequisite") {
		t.Fatal("prerequisites are not told apart from steps after landing:", prompt)
	}
	own.Plan = &core.Plan{Summary: "Go", Exists: []string{"Existing library"}, Prerequisites: []core.Prerequisite{{What: "Library tagged", Outcome: "confirmed"}, {What: "Owner check", Outcome: "dropped"}}}
	shown := planText(own)
	if strings.Index(shown, "Prerequisites:") > strings.Index(shown, "What already exists:") {
		t.Fatal(shown)
	}
	if !strings.Contains(shown, "Library tagged (confirmed)") || !strings.Contains(shown, "Owner check (dropped)") {
		t.Fatal(shown)
	}
	own.DependsOn = []string{other.ID}
	own.WaitingOn = []core.WaitOn{{Task: other.ID, Ref: other.Ref, ProjectID: "b", Project: "Library"}}
	if got := waitsLine([]core.Task{same}, own); got != "LIB-1 (in Library; stays)" {
		t.Fatal(got)
	}
	own.Blockers = []core.Blocker{{Kind: core.BlockerPrerequisite, Description: "Library tagged", Outcome: "confirmed"}}
	if got := researcherPrompt(projects[0], own, tasks, projects); !strings.Contains(got, "Existing prerequisite: Library tagged (confirmed)") {
		t.Fatal(got)
	}
}

func TestWaitSummaryWithExistingDependencyNeedsNoSendBack(t *testing.T) {
	t.Parallel()
	a, runner, p := plannedCode(t, 6, `{"summary":"Implementation waits for existing work"}`)
	ctx := context.Background()
	dep, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Dependency"})
	queued, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use dependency", DependsOn: []string{dep.ID}})
	// Research may discover an existing dependency after a task was admitted.
	a.Core.UpdateTask(ctx, queued.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskResearching
		t.Playbook = p.Playbook
		t.Roles = p.Playbook.Roles
		return "", nil
	})
	snap, _ := a.Core.Snapshot(ctx)
	task, _ := findTask(snap, p.ID, queued.ID)
	m, err := a.mediumFor(ctx, p, task.Playbook)
	if err != nil {
		t.Fatal(err)
	}
	researcher, _ := task.Researcher()
	if err := a.researchTask(ctx, p, task, m, researcher); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, spec := range runner.seen {
		if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("research ran %d times", count)
	}
}

// These describe product behaviour or deny a wait, rather than hold implementation.
var featureSummaries = []string{
	"Add a spinner that waits for the server, then shows a ready state",
	"Make the CLI wait for the lock to be released",
	"homework wait screen",
	"Add a work wait screen",
	"Nothing is blocked on this",
	"Implementation does not wait for anything",
	"such a task waits in To do and is released when the other task lands",
	"show on the board when the task waits for the PM",
	"when a task is blocked by a manual blocker the card says so",
	"A task waits in To do until its dependency lands",
	"Show why a task is blocked by a manual condition",
	"network waits for a reply",
	"framework waits for initialization",
	"Implementation waited for a tag",
	"background work will wait for the lock to be released",
	"Background work should wait for the server to be ready",
	"Background work must wait for the lock to be released",
	"Implement the feature",
}

func TestFeatureSummariesResearchOnce(t *testing.T) {
	t.Parallel()
	for _, summary := range featureSummaries {
		t.Run(summary, func(t *testing.T) {
			t.Parallel()
			reply := fmt.Sprintf(`{"summary":%q}`, summary)
			a, runner, p := plannedCode(t, 6, reply)
			queued, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Feature"})
			got := taskNow(t, a, queued.ID)
			count := 0
			for _, spec := range runner.seen {
				if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
					count++
				}
			}
			if count != 1 || len(got.Blockers) != 0 || got.Plan == nil || len(got.Plan.Questions) != 0 || runner.edits == 0 {
				t.Fatalf("research count %d task %+v edits %d", count, got, runner.edits)
			}
		})
	}
}

func TestResearcherOnlyShowsNonemptyTaskGroups(t *testing.T) {
	t.Parallel()
	p := core.Project{ID: "app", Title: "App"}
	lib := core.Project{ID: "lib", Title: "Library"}
	own := core.Task{ID: "own", ProjectID: p.ID}
	local := core.Task{ID: "local", ProjectID: p.ID, Objective: "Local"}
	remote := core.Task{ID: "remote", ProjectID: lib.ID, Objective: "Remote"}
	for _, tc := range []struct {
		others        []core.Task
		local, remote bool
	}{
		{nil, false, false}, {[]core.Task{local}, true, false}, {[]core.Task{remote}, false, true}, {[]core.Task{local, remote}, true, true},
	} {
		prompt := researcherPrompt(p, own, tc.others, []core.Project{p, lib})
		if strings.Contains(prompt, "A plan may depend on any task listed above.") != (tc.local || tc.remote) {
			t.Fatal(prompt)
		}
		if strings.Contains(prompt, "The project's other unfinished tasks:") != tc.local || strings.Contains(prompt, "Unfinished tasks in your other projects:") != tc.remote {
			t.Fatal(prompt)
		}
	}
	own.Blockers = []core.Blocker{{Kind: core.BlockerPrerequisite, Description: "Library tagged"}}
	prompt := researcherPrompt(p, own, nil, []core.Project{p, lib})
	if !strings.Contains(prompt, "Existing prerequisite: Library tagged (waiting for the owner)") || strings.Contains(prompt, "()") {
		t.Fatal(prompt)
	}
}

func TestClearedPrerequisiteCountsAsDeclaredWait(t *testing.T) {
	t.Parallel()
	now := core.Task{}.CreatedAt
	for _, outcome := range []string{"confirmed", "dropped"} {
		task := core.Task{Blockers: []core.Blocker{{Kind: core.BlockerPrerequisite, Outcome: outcome, ClearedAt: &now}}}
		if core.UndeclaredPlanWait(core.Plan{Summary: "Implementation waits for lib v0.20.0, now confirmed"}, nil, task) {
			t.Fatal(outcome)
		}
	}
}

func TestInvalidResearchDependencyIsSentBack(t *testing.T) {
	t.Parallel()
	missing := `{"summary":"Implementation waits for LIB-999 to land","depends_on":["LIB-999"]}`
	a, runner, p := plannedCode(t, 6, missing, missing)
	queued, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Use dependency"})
	got := taskNow(t, a, queued.ID)
	if got.Status != core.TaskWaiting || got.Plan == nil || !slices.Contains(got.Plan.Questions, core.UndeclaredWaitQuestion) || runner.edits != 0 {
		t.Fatalf("task %+v edits %d", got, runner.edits)
	}
	count := 0
	for _, spec := range runner.seen {
		if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
			count++
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
}

func TestResolvedPrerequisiteResearchesOnce(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"confirmed", "dropped"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			a, runner, p := plannedCode(t, 6, `{"summary":"Implementation waits for lib v0.20.0, now confirmed"}`)
			ctx := context.Background()
			queued, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use release"})
			a.Core.UpdateTask(ctx, queued.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Status, task.Playbook, task.Roles = core.TaskResearching, p.Playbook, p.Playbook.Roles
				now := task.CreatedAt
				task.Blockers = []core.Blocker{{ID: "release", Kind: core.BlockerPrerequisite, Description: "lib v0.20.0", Outcome: outcome, ClearedAt: &now}}
				return "", nil
			})
			snap, _ := a.Core.Snapshot(ctx)
			task, _ := findTask(snap, p.ID, queued.ID)
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			researcher, _ := task.Researcher()
			if err := a.researchTask(ctx, p, task, m, researcher); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, spec := range runner.seen {
				if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
					count++
				}
			}
			snap, _ = a.Core.Snapshot(ctx)
			got, _ := findTask(snap, p.ID, queued.ID)
			if count != 1 || got.Status != core.TaskWriting || got.Plan == nil || len(got.Plan.Questions) != 0 || got.Plan.Prerequisites[0].Outcome != outcome {
				t.Fatalf("count %d task %+v", count, got)
			}
		})
	}
}

func TestBegunTaskWaitSummaryReturnsToItsChecker(t *testing.T) {
	t.Parallel()
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "fallback"}[malformed], func(t *testing.T) {
			t.Parallel()
			reply := `{"summary":"Implementation waits for the library to be tagged"}`
			if malformed {
				reply = "Implementation waits for the library to be tagged."
			}
			a, runner, p := plannedCode(t, 0, plainPlan, reply, reply)
			runner.reviews = []string{`{"outcome":"research","summary":"Check the API.","findings":[],"question":"Does the v2 API cover this?"}`, pass, pass}
			ctx := context.Background()
			queued, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use API"})
			if err != nil {
				t.Fatal(err)
			}
			before := stepUntil(t, a, queued.ID, func(task core.Task) bool {
				return task.Status == core.TaskResearching && len(task.Revisions) > 0
			})
			if before.OpenResearch() == nil {
				t.Fatal("no open checker request")
			}
			got := taskNow(t, a, queued.ID)
			if got.Plan == nil || len(got.Plan.Questions) != 0 || len(got.Revisions) != 1 || runner.edits != 1 || got.Branch != before.Branch || got.Base != before.Base {
				t.Fatalf("did not preserve the checked draft: %+v edits %d", got, runner.edits)
			}
			if d := openDecision(t, a, got); d.Kind != core.DecisionDelivery {
				t.Fatalf("asked the owner instead of returning to the checker: %+v", d)
			}
			if got.OpenResearch() != nil || got.Research[0].AnsweredAt.IsZero() || got.Research[0].From != "Reviewer" {
				t.Fatalf("research request: %+v", got.Research)
			}
			plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
			want := 2
			if malformed {
				want++
			}
			if len(plans) != want {
				t.Fatalf("research turns %d, want %d", len(plans), want)
			}
		})
	}
}

func TestAnsweredWaitQuestionIsNotAskedAgain(t *testing.T) {
	t.Parallel()
	for _, typed := range []bool{false, true} {
		for _, malformed := range []bool{false, true} {
			name := fmt.Sprintf("typed=%v/fallback=%v", typed, malformed)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				missing := `{"summary":"Implementation waits for the library to be tagged"}`
				reply := missing
				if malformed {
					reply = "Implementation waits for the library to be tagged."
				}
				a, runner, p := plannedCode(t, 6, missing, missing, reply, reply)
				ctx := context.Background()
				queued, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Use release"})
				if err != nil {
					t.Fatal(err)
				}
				waiting := taskNow(t, a, queued.ID)
				if waiting.Plan == nil || !slices.Contains(waiting.Plan.Questions, core.UndeclaredWaitQuestion) {
					t.Fatalf("missing question: %+v", waiting)
				}
				d := openDecision(t, a, waiting)
				if typed {
					_, err = a.Core.AnswerDecision(ctx, d.ID, "Start the work now")
				} else {
					_, err = a.Core.ChooseDecision(ctx, d.ID, "Use your judgment")
				}
				if err != nil {
					t.Fatal(err)
				}
				got := taskNow(t, a, queued.ID)
				if got.Plan == nil || len(got.Plan.Questions) != 0 || len(got.Revisions) != 1 || runner.edits != 1 {
					t.Fatalf("did not start after the answer: %+v edits %d", got, runner.edits)
				}
				if d := openDecision(t, a, got); d.Kind != core.DecisionDelivery {
					t.Fatalf("asked again: %+v", d)
				}
				plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
				want := 3
				if malformed {
					want++
				}
				if len(plans) != want {
					t.Fatalf("research turns %d, want %d", len(plans), want)
				}
				snap, err := a.Core.Snapshot(ctx)
				if err != nil {
					t.Fatal(err)
				}
				questions := 0
				for _, decision := range snap.Decisions {
					if decision.TaskID == got.ID && decision.Kind == core.DecisionQuestion {
						questions++
					}
				}
				if questions != 1 {
					t.Fatalf("opened %d owner questions", questions)
				}
			})
		}
	}
}
