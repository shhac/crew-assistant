package work

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestPMDuplicateToolOwnershipBoundAndLiveDisablement(t *testing.T) {
	lp, p, first := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	seen := lp.managerTools(p.ID, core.Role{Name: "Manager"})
	if !seen.offers("close_duplicate") || lp.answerTools(p.ID, core.Role{}).offers("close_duplicate") || lp.toolsFor(first, core.RoleImplementer, core.Role{}).offers("close_duplicate") {
		t.Fatal("policy ownership")
	}
	tasks := []core.Task{first}
	for range 4 {
		q, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Accidental duplicate", Criteria: []string{"Works"}})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, q)
	}
	seen = lp.managerTools(p.ID, core.Role{Name: "Manager"})
	for i := 1; i <= 4; i++ {
		result := callTool(t, seen, "close_duplicate", map[string]string{"task_id": tasks[i].ID, "duplicate_of": first.ID, "reason": "Same outcome"})
		if result.IsError != (i == 4) {
			t.Fatalf("%d: %+v", i, result)
		}
	}
	off := false
	if err := lp.Core.SetPMPolicy(ctx, "project", p.ID, "close_duplicate", &off, 0, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	if lp.managerTools(p.ID, core.Role{}).offers("close_duplicate") {
		t.Fatal("off tool offered")
	}
	stale := lp.managerTools(p.ID, core.Role{})
	stale.duplicateTasks = seen.duplicateTasks
	stale.duplicates = new(int)
	if result := callTool(t, stale, "close_duplicate", map[string]string{"task_id": tasks[4].ID, "duplicate_of": first.ID, "reason": "Same"}); !result.IsError {
		t.Fatal("stale policy authority")
	}
}
func TestPMPromptsShowLoopEvidenceOnlyForLoops(t *testing.T) {
	lp, p, first := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	snap, _ := lp.Core.Snapshot(ctx)
	var b strings.Builder
	pmTasks(&b, snap, p)
	if strings.Contains(b.String(), "Loop signals:") {
		t.Fatal(b.String())
	}
	for range 3 {
		_, err := lp.Core.UpdateTask(ctx, first.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskDeciding
			t.DecisionID = ""
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = lp.Core.OpenTaskDecision(ctx, first.ID, core.DecisionEscalation, core.DecisionInput{Title: "Same escalation", Context: "Unresolved", Recommendation: "Retry", Choices: []string{"Retry", "Stop"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, _ = lp.Core.Snapshot(ctx)
	b.Reset()
	pmTasks(&b, snap, p)
	if !strings.Contains(b.String(), "3rd identical escalation") {
		t.Fatal(b.String())
	}
	t0, _ := findTask(snap, p.ID, first.ID)
	t0.Revisions = []core.Revision{{N: 1}}
	if got := escalationPrompt(snap, p, t0, nil, nil, false, nil); !strings.Contains(got, "Loop signals:") {
		t.Fatal(got)
	}
	for _, kind := range []string{core.DecisionFailure, core.DecisionQuestion} {
		for range 3 {
			_, _ = lp.Core.UpdateTask(ctx, first.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.DecisionID = ""
				return "", nil
			})
			d, err := lp.Core.OpenTaskDecision(ctx, first.ID, kind, core.DecisionInput{Title: "Repeated " + kind, Context: "Same question", Recommendation: "Retry", Choices: []string{"Retry", "Stop"}})
			if err != nil {
				t.Fatal(err)
			}
			if d.ID == "" {
				t.Fatal(fmt.Sprint(d))
			}
		}
	}
	snap, _ = lp.Core.Snapshot(ctx)
	last := snap.Decisions[len(snap.Decisions)-1]
	if last.LoopReport == nil || !strings.Contains(last.Context, "same question 3 times") {
		t.Fatal(last)
	}
}

func TestPMChatDuplicateReceipt(t *testing.T) {
	lp, p, first := loopApp(t, &scriptedRunner{}, "")
	duplicate, err := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Repeated request", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	tools := lp.managerTools(p.ID, core.Role{Name: "PM"})
	tools.chat = &pmChatChanges{}
	result := callTool(t, tools, "close_duplicate", map[string]string{"task_id": duplicate.ID, "duplicate_of": first.ID, "reason": "Same outcome"})
	if result.IsError {
		t.Fatal(result)
	}
	receipt := tools.chat.snapshot()
	if len(receipt) != 1 || receipt[0].Kind != "closed_duplicate" || len(receipt[0].Tasks) != 2 {
		t.Fatal(receipt)
	}
}

func TestPMDuplicateFencesTheRecordActuallyShownInThePrompt(t *testing.T) {
	lp, p, first := loopApp(t, &scriptedRunner{}, "")
	second, err := lp.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Duplicate", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	shown, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = lp.Core.EditTask(context.Background(), core.EditInput{Project: p.ID, Task: second.ID, By: core.FromOwner, Kind: core.FromOwner, Add: []string{"A new owner requirement"}})
	if err != nil {
		t.Fatal(err)
	}
	tools := lp.managerTools(p.ID, core.Role{Name: "Manager"}, shown)
	result := callTool(t, tools, "close_duplicate", map[string]string{"task_id": second.ID, "duplicate_of": first.ID, "reason": "Same"})
	if !result.IsError {
		t.Fatal("silently adopted an owner edit made after prompt construction")
	}
}

func TestPMQueueLoopSummaryDoesNotCarryFullDecisionHistory(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p := core.Project{ID: "p", Title: "Synthetic"}
	task := core.Task{ID: "t", ProjectID: "p", Objective: "Core purpose", Status: core.TaskReviewing, Round: 4, MaxRounds: 4, Attempt: 8, Revisions: []core.Revision{{N: 1}}}
	snap := core.Snapshot{Projects: []core.Project{p}, Tasks: []core.Task{task}}
	for i := range 622 {
		snap.Decisions = append(snap.Decisions, core.Decision{ID: fmt.Sprintf("ordinary-%03d", i), TaskID: task.ID, Kind: core.DecisionChoice, Title: "Normal choice", CreatedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	for i := range 3 {
		snap.Decisions = append(snap.Decisions, core.Decision{ID: fmt.Sprintf("escalation-%d", i), TaskID: task.ID, Kind: core.DecisionEscalation, Title: "Same escalation", CreatedAt: at.Add(time.Duration(622+i) * time.Minute)})
	}
	var b strings.Builder
	pmTasks(&b, snap, p)
	queue := b.String()
	if !strings.Contains(queue, "count 3; threshold 2") || !strings.Contains(queue, "escalation-2") {
		t.Fatal(queue)
	}
	if strings.Contains(queue, "ordinary-") || strings.Contains(queue, "escalation-0") || strings.Contains(queue, "accept_leftovers (") || len(queue) > 1200 {
		t.Fatal("queue carries full history or policy catalogue", queue)
	}
	full := escalationPrompt(snap, p, task, nil, nil, false, nil)
	if strings.Contains(full, "ordinary-000") || !strings.Contains(full, "ordinary-621") || !strings.Contains(full, "latest 3 of 622 sources") || !strings.Contains(full, "escalation-0") || !strings.Contains(full, "accept_leftovers (") {
		t.Fatal("escalation lost full evidence", full)
	}
}

// Drive the real producers: their choice and reason must survive annotation.
func TestLoopDecisionsPreserveProducerRecommendations(t *testing.T) {
	ctx := context.Background()
	t.Run("judged escalation", func(t *testing.T) {
		runner := &scriptedRunner{reviews: []string{revise}, escalate: []string{judgement(true, false, false, false, false), judgement(true, false, false, false, false), judgement(true, false, false, false, false)}}
		lp, p, task := roundLimited(t, runner, true)
		off := false
		if err := lp.Core.SetPMPolicy(ctx, "project", p.ID, "pause_loop", &off, 0, core.FromOwner); err != nil {
			t.Fatal(err)
		}
		task = stepUntil(t, lp, task.ID, waiting)
		first := openDecision(t, lp, task)
		var err error
		for range 2 {
			task, err = lp.Core.UpdateTask(ctx, task.ID, func(q *core.Task, _ *core.Project) (string, error) {
				q.Status = core.TaskDeciding
				q.DecisionID = ""
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = lp.escalate(ctx, p, task, task.Verdicts, task.Verdicts); err != nil {
				t.Fatal(err)
			}
		}
		snap, _ := lp.Core.Snapshot(ctx)
		d := snap.Decisions[len(snap.Decisions)-1]
		if d.LoopReport == nil || !strings.HasPrefix(d.Recommendation, first.Recommendation) || !strings.Contains(d.Recommendation, "Pim's reason") || !strings.Contains(d.Recommendation, "is off; no action was taken") || !strings.Contains(d.Context, "3rd identical escalation") {
			t.Fatal(d)
		}
		if got := turns(runner, "Judge what remains at the round limit"); len(got) != 3 || !strings.Contains(got[2].Prompt, "Loop signals:") {
			t.Fatal("judge did not see loop evidence", got)
		}
	})
	t.Run("failure limit", func(t *testing.T) {
		lp, p, task := loopApp(t, &scriptedRunner{}, "")
		off := false
		if err := lp.Core.SetPMPolicy(ctx, "project", p.ID, "pause_loop", &off, 0, core.FromOwner); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			var err error
			task, err = lp.Core.UpdateTask(ctx, task.ID, func(q *core.Task, _ *core.Project) (string, error) {
				q.Status = core.TaskWriting
				q.DecisionID = ""
				q.Failures = roleRetries
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = lp.roleFailed(ctx, task, "Writer", fmt.Errorf("synthetic workspace failure")); err != nil {
				t.Fatal(err)
			}
		}
		snap, _ := lp.Core.Snapshot(ctx)
		d := snap.Decisions[len(snap.Decisions)-1]
		if d.LoopReport == nil || !strings.HasPrefix(d.Recommendation, choiceTryAgain+" once the cause is fixed") || !strings.Contains(d.Recommendation, "is off; no action was taken") {
			t.Fatal(d)
		}
	})
	t.Run("accepted catch-up limit", func(t *testing.T) {
		lp, p, task := loopApp(t, &scriptedRunner{}, "")
		var err error
		task, err = lp.Core.UpdateTask(ctx, task.ID, func(q *core.Task, _ *core.Project) (string, error) {
			q.Status = core.TaskLanding
			q.CatchUps = maxCatchUps
			q.Revisions = []core.Revision{{N: 1, At: q.CreatedAt}, {N: 2, CleanMergeOf: 1, At: q.CreatedAt.Add(time.Minute)}, {N: 3, CleanMergeOf: 2, At: q.CreatedAt.Add(2 * time.Minute)}}
			q.Acceptance = &core.DraftAcceptance{Revision: 1, BriefVersion: p.Brief.Version, TextVersion: q.TextVersion}
			q.Approved = 1
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// At the limit the producer must ask the owner before invoking the adapter.
		if err = lp.catchUpRound(ctx, task, nil, line{What: "another change landed"}); err != nil {
			t.Fatal(err)
		}
		snap, _ := lp.Core.Snapshot(ctx)
		d := snap.Decisions[len(snap.Decisions)-1]
		if d.LoopReport == nil || !strings.HasPrefix(d.Recommendation, choiceTryAgain+" once the target is quiet") || !strings.Contains(d.Context, "catch up with other work 5 times") {
			t.Fatal(d)
		}
		mechanical, ordinary := false, false
		for _, s := range d.LoopReport.Signals {
			if s.Kind == "catch_up_attempts" {
				mechanical = s.Tripped && s.Count == 5 && len(s.Sources) == 3
			}
			if s.Kind == "catch_ups" {
				ordinary = !s.Tripped && s.Count == 2
			}
		}
		if !mechanical || !ordinary || !strings.Contains(d.LoopReport.Recommend(snap, p.ID), "preserve accepted-draft checked landing") {
			t.Fatal(d.LoopReport)
		}
	})
}
