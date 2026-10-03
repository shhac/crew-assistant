package work

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

const handOnReply = "\n```hand-on\n{\"why\":\"A fresh view would help\"}\n```"

func TestHandOnRecordedWithWriterAndChecker(t *testing.T) {
	for _, kind := range []string{core.RoleImplementer, core.RoleReviewer} {
		t.Run(kind, func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{pass}}
			if kind == core.RoleImplementer {
				runner.writerText = "Done." + handOnReply
			} else {
				runner.reviews = []string{pass + handOnReply}
			}
			a, _, _ := loopApp(t, runner, "")
			task := settle(t, a)
			if len(task.HandOn) != 1 || task.HandOn[0].Kind != kind || task.HandOn[0].Why != "A fresh view would help" {
				t.Fatalf("request: %+v", task.HandOn)
			}
			if len(task.Revisions) != 1 || task.Revisions[0].Seat != "Writer" || len(task.Verdicts) != 1 {
				t.Fatalf("turn record: %+v", task)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			found := false
			for _, entry := range snap.Activity {
				if entry.TaskID == task.ID && entry.Kind == "task.hand_on" && strings.Contains(entry.Summary, "A fresh view would help") {
					found = true
				}
			}
			if !found {
				t.Fatal("hand-on reason missing from activity")
			}
		})
	}
}

func TestHandOnRecordedWithResearch(t *testing.T) {
	a, _, p := plannedCode(t, 6, plainPlan+handOnReply)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	task = taskNow(t, a, task.ID)
	if task.Plan == nil || len(task.HandOn) != 1 || task.HandOn[0].Kind != core.RoleResearcher {
		t.Fatalf("plan and request: %+v", task)
	}
}

func TestStoppedWriterRecordsNeitherDraftNorHandOn(t *testing.T) {
	runner := &scriptedRunner{writerText: "Done." + handOnReply}
	a, _, task := loopApp(t, runner, "")
	runner.onWriter = func(string) {
		_, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskStopped
			return "Stopped", nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	task = settle(t, a)
	if len(task.Revisions) != 0 || len(task.HandOn) != 0 {
		t.Fatalf("stale turn recorded: %+v", task)
	}
}

func TestMalformedHandOnIsReportedAndIgnored(t *testing.T) {
	for _, block := range []string{"{", `{"why":""}`, `{"why":17}`, `null`} {
		if why, problems := parseHandOn(block); why != "" || len(problems) != 1 {
			t.Fatalf("%q: why=%q problems=%v", block, why, problems)
		}
	}
	runner := &scriptedRunner{writerText: "Done.\n```hand-on\n{\n```", reviews: []string{pass}}
	a, _, _ := loopApp(t, runner, "")
	task := settle(t, a)
	if len(task.HandOn) != 0 || len(task.WakeErrors) != 1 {
		t.Fatalf("malformed: %+v", task)
	}
}

func TestResearchPassCanHandOnAndKeepsBuildersWakeErrors(t *testing.T) {
	research := `{"outcome":"research","summary":"Check v2.","findings":[],"question":"Does v2 cover this?"}`
	a, runner, p := plannedCode(t, 0, plainPlan, plainPlan+handOnReply)
	pb := *p.Playbook
	pb.Roles = append([]core.Role{}, pb.Roles...)
	pb.AddSeat("Researcher")
	if _, err := a.Core.SetPlaybook(context.Background(), p.ID, pb); err != nil {
		t.Fatal(err)
	}
	runner.reviews = []string{research, pass, pass}
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
	task = stepUntil(t, a, task.ID, func(task core.Task) bool { return task.Status == core.TaskResearching && len(task.Revisions) > 0 })
	_, err := a.Core.UpdateTask(context.Background(), task.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.WakeErrors = []string{"builder wake problem"}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	task = taskNow(t, a, task.ID)
	if len(task.HandOn) != 1 || task.HandOn[0].Kind != core.RoleResearcher || len(task.WakeErrors) != 1 || task.WakeErrors[0] != "builder wake problem" {
		t.Fatalf("research pass: %+v", task)
	}
	plans := turns(&runner.scriptedRunner, "Plan this task before anything is written")
	if len(plans) != 2 || !strings.Contains(plans[1].Prompt, "```hand-on") {
		t.Fatal("research pass did not offer hand-on")
	}
}

func TestNonWriterBlockProblemsAreLoggedWithoutChangingWakeErrors(t *testing.T) {
	broken := "\n```hand-on\n{\n```"
	for _, role := range []string{"research", "design request", "checker"} {
		t.Run(role, func(t *testing.T) {
			a, runner, p := plannedCode(t, 0, plainPlan)
			runner.reviews = []string{pass, pass}
			if role == "checker" {
				runner.reviews[0] += broken
			}
			if role == "research" {
				runner.plans = []string{plainPlan + broken}
			}
			if role == "design request" {
				seatDesigner(t, a, p.ID)
				runner.plans = []string{`{"design":"Which layout?"}` + broken, plainPlan}
			}
			task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
			// Preserve an actual builder diagnostic through research and checks;
			// the writer's own new round may replace it, so inspect at the outcome.
			if role == "checker" {
				task = stepUntil(t, a, task.ID, func(task core.Task) bool { return task.Status == core.TaskReviewing })
			}
			_, err := a.Core.UpdateTask(context.Background(), task.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.WakeErrors = []string{"builder wake problem"}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			task = stepUntil(t, a, task.ID, func(task core.Task) bool {
				if role == "checker" {
					return len(task.Verdicts) > 0
				}
				if role == "design request" {
					return task.WithDesigner
				}
				return task.Plan != nil
			})
			if len(task.WakeErrors) != 1 || task.WakeErrors[0] != "builder wake problem" || len(task.HandOn) != 0 {
				t.Fatalf("%s: wake=%v hand=%v", role, task.WakeErrors, task.HandOn)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			found := false
			for _, entry := range snap.Activity {
				if entry.TaskID == task.ID && entry.Kind == "task.reply_problem" && strings.Contains(entry.Summary, "hand-on block ignored") {
					found = true
				}
			}
			if !found {
				t.Fatal("role's malformed block not logged")
			}
		})
	}
}

func TestFailedFirstBuildKeepsClaimant(t *testing.T) {
	runner := &scriptedRunner{fail: []error{errors.New("provider unavailable")}, reviews: []string{pass}}
	a, p, queued := loopApp(t, runner, "")
	pb := *p.Playbook
	pb.AddSeat("Writer")
	if _, err := a.Core.SetPlaybook(context.Background(), p.ID, pb); err != nil {
		t.Fatal(err)
	}
	failed := settle(t, a)
	if failed.Failures != 1 || len(failed.Revisions) != 0 {
		t.Fatalf("failed: %+v", failed)
	}
	first := failed.FirstSeats[core.RoleImplementer]
	snap, _ := a.Core.Snapshot(context.Background())
	cursor := snap.Projects[0].SeatRotation[core.RoleImplementer]
	_, err := a.Core.UpdateTask(context.Background(), queued.ID, func(task *core.Task, _ *core.Project) (string, error) { task.RetryAt = time.Time{}; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	done := settle(t, a)
	if len(done.Revisions) != 1 || done.Revisions[0].Seat != first {
		t.Fatalf("retry: %+v", done.Revisions)
	}
	snap, _ = a.Core.Snapshot(context.Background())
	if snap.Projects[0].SeatRotation[core.RoleImplementer] != cursor {
		t.Fatal("failure used another rotation turn")
	}
}

func TestDesignQuestionsDoNotHandOnAnUnfinishedRound(t *testing.T) {
	t.Run("writer", func(t *testing.T) {
		runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign + handOnReply, "Done."}}
		a, p, queued := loopApp(t, runner, "")
		seatDesigner(t, a, p.ID)
		paused := stepUntil(t, a, queued.ID, withDesigner)
		if len(paused.HandOn) != 0 || !strings.Contains(strings.Join(paused.WakeErrors, " "), "hand-on ignored") {
			t.Fatalf("paused: hand=%v problems=%v", paused.HandOn, paused.WakeErrors)
		}
		done := settle(t, a)
		if len(done.Revisions) != 1 || done.Revisions[0].Seat != "Writer" {
			t.Fatalf("resumed: %+v", done.Revisions)
		}
	})
	t.Run("researcher", func(t *testing.T) {
		a, _, p := plannedCode(t, 6, `{"design":"Which layout?"}`+handOnReply, plainPlan)
		seatDesigner(t, a, p.ID)
		queued, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add A"})
		if err != nil {
			t.Fatal(err)
		}
		paused := stepUntil(t, a, queued.ID, withDesigner)
		if len(paused.HandOn) != 0 {
			t.Fatalf("unfinished round handed on: %+v", paused.HandOn)
		}
		snap, _ := a.Core.Snapshot(context.Background())
		found := false
		for _, entry := range snap.Activity {
			if entry.TaskID == queued.ID && entry.Kind == "task.reply_problem" && strings.Contains(entry.Summary, "hand-on ignored") {
				found = true
			}
		}
		if !found {
			t.Fatal("research design hand-on was not reported")
		}
	})
}
