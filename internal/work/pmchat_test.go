package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

type pmChatRunner struct {
	run func(context.Context, roles.Spec) (roles.Result, error)
}

func (r pmChatRunner) Run(ctx context.Context, s roles.Spec) (roles.Result, error) {
	return r.run(ctx, s)
}
func TestPMChatRepliesInProjectWhilePaused(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{false: "project", true: "all"}[global], func(t *testing.T) {
			lp, p, first, _ := pmTeam(t, &scriptedRunner{})
			ctx := context.Background()
			if global {
				lp.Core.SetPaused(ctx, true)
			} else {
				lp.Core.SetProjectPaused(ctx, p.ID, true)
			}
			other, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Other", Template: "draft", Brief: core.BriefInput{Goal: "Other"}})
			if err != nil {
				t.Fatal(err)
			}
			lp.runner = pmChatRunner{run: func(_ context.Context, s roles.Spec) (roles.Result, error) {
				for _, want := range []string{p.Brief.Goal, first.Objective, "Team: ", "Pim (pm)", "New question"} {
					if !strings.Contains(s.Prompt, want) {
						t.Errorf("prompt lacks %q", want)
					}
				}
				return roles.Result{Text: "The note comes first."}, nil
			}}
			if _, err = lp.SendPMChat(ctx, p.ID, "msg", "New question"); err != nil {
				t.Fatal(err)
			}
			step(t, lp)
			log, err := lp.Core.PMChat(ctx, p.ID)
			if err != nil || len(log) != 2 || log[1].Text != "The note comes first." || log[0].Status != "answered" {
				t.Fatal(log, err)
			}
			log, _ = lp.Core.PMChat(ctx, other.ID)
			if len(log) != 0 {
				t.Fatal(log)
			}
			snap, _ := lp.Core.Snapshot(ctx)
			if !strings.Contains(pmPrompt(snap, p), "The note comes first.") {
				t.Fatal("look forgot chat")
			}
			task, _ := findTask(snap, p.ID, first.ID)
			if task.Status != core.TaskQueued {
				t.Fatal("paused work started")
			}
		})
	}
}
func TestPMChatToolsAndPartialFailureReceipts(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "partial failure"}[fail], func(t *testing.T) {
			lp, p, first, second := pmTeam(t, &scriptedRunner{})
			ctx := context.Background()
			lp.Core.SetPaused(ctx, true)
			lp.Core.ApplyPM(ctx, p.ID, core.PMAnswer{Triage: []core.TriageRelease{{Task: second.ID, To: core.TriageToResearch}}})
			calls := 0
			lp.runner = pmChatRunner{run: func(ctx context.Context, s roles.Spec) (roles.Result, error) {
				calls++
				if strings.Index(s.Prompt, first.Objective) > strings.Index(s.Prompt, second.Objective) {
					t.Fatal("prompt lost queue order")
				}
				tool := func(name string, args map[string]string) {
					raw, _ := json.Marshal(args)
					result, err := s.Handler.CallTool(ctx, session.ToolCall{Name: name, Arguments: raw})
					if err != nil || result.IsError {
						t.Fatalf("%s: %+v %v", name, result, err)
					}
				}
				if calls == 1 {
					tool("order_tasks", map[string]string{"task_ids": second.ID + "," + first.ID})
					tool("link_tasks", map[string]string{"task_id": first.ID, "other_task_id": second.ID, "relation": "depends_on"})
					tool("queue_task", map[string]string{"title": "Follow-up", "requirements": "Clear", "depends_on": ""})
				}
				if fail {
					return roles.Result{}, errors.New("private provider failure")
				}
				return roles.Result{Text: "Changed the order."}, nil
			}}
			lp.SendPMChat(ctx, p.ID, "msg", "Put the second note first")
			step(t, lp)
			log, _ := lp.Core.PMChat(ctx, p.ID)
			var receipt []core.PMChatChange
			if fail {
				if log[0].Status != "failed" || strings.Contains(log[0].Error, "private") {
					t.Fatal(log)
				}
				receipt = log[0].Changes
			} else {
				if len(log) != 2 {
					t.Fatal(log)
				}
				receipt = log[1].Changes
			}
			if len(receipt) != 3 || receipt[0].Kind != "reordered" || receipt[1].Kind != "linked" || receipt[2].Kind != "queued" || len(receipt[2].Tasks) != 1 {
				t.Fatal(receipt)
			}
			snap, _ := lp.Core.Snapshot(ctx)
			task, _ := findTask(snap, p.ID, first.ID)
			if len(task.DependsOn) != 1 || task.DependsOn[0] != second.ID {
				t.Fatal(task)
			}
		})
	}
}
func TestPMChatWaitsOnBusySeatAndSeesEarlierReplies(t *testing.T) {
	lp, p, _, _ := pmTeam(t, &scriptedRunner{})
	ctx := context.Background()
	lp.Core.SetPaused(ctx, true)
	c, _, ok, err := lp.Core.ClaimPMQuestion(ctx, p.ID, func(core.Role) string { return "" })
	if !ok || err != nil {
		t.Fatal(err)
	}
	lp.SendPMChat(ctx, p.ID, "first", "First question")
	step(t, lp)
	log, _ := lp.Core.PMChat(ctx, p.ID)
	if log[0].Status != "waiting" {
		t.Fatal(log)
	}
	lp.Core.ReleaseProjectClaim(ctx, p.ID, c.Token)
	calls := 0
	lp.runner = pmChatRunner{run: func(_ context.Context, s roles.Spec) (roles.Result, error) {
		calls++
		if calls == 1 && strings.Contains(s.Prompt, "Second question") {
			t.Fatal("turn saw a later queued question")
		}
		current := "First question"
		if calls == 2 {
			current = "Second question"
		}
		if strings.Count(s.Prompt, current) != 1 {
			t.Fatal("current question repeated in history")
		}
		if calls == 2 && strings.Count(s.Prompt, "Earlier reply") != 1 {
			t.Fatal("missing conversation")
		}
		return roles.Result{Text: "Earlier reply"}, nil
	}}
	lp.SendPMChat(ctx, p.ID, "second", "Second question")
	step(t, lp)
	step(t, lp)
	log, _ = lp.Core.PMChat(ctx, p.ID)
	if len(log) != 4 || calls != 2 {
		t.Fatal(log, calls)
	}
}

func TestPMChatOrderConflictAddsNoReceiptAndToolsStayScoped(t *testing.T) {
	lp, p, first, second := pmTeam(t, &scriptedRunner{})
	ctx := context.Background()
	changes := &pmChatChanges{}
	tools := lp.managerTools(p.ID, core.Role{Name: "Pim"})
	tools.chat = changes
	result := callTool(t, tools, "order_tasks", map[string]string{"task_ids": "missing"})
	if !result.IsError || !strings.Contains(result.Content, "the to-do list changed; try again") || len(changes.snapshot()) != 0 {
		t.Fatal(result, changes.snapshot())
	}
	plain := lp.managerTools(p.ID, core.Role{Name: "Pim"})
	if !callTool(t, plain, "order_tasks", map[string]string{"task_ids": first.ID + "," + second.ID}).IsError {
		t.Fatal("ordering leaked into ordinary manager tools")
	}
	other, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Other", Template: "draft", Brief: core.BriefInput{Goal: "Other"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := lp.Core.QueueTask(ctx, other.ID, core.TaskInput{Objective: "Other task"})
	if err != nil {
		t.Fatal(err)
	}
	if !callTool(t, tools, "edit_task", map[string]string{"task_id": task.ID, "title": "Changed", "requirements": ""}).IsError || len(changes.snapshot()) != 0 {
		t.Fatal("changed other project")
	}
}

func TestPMChatWaitsForPMUsageAllowance(t *testing.T) {
	lp, p, _, _ := pmTeam(t, &scriptedRunner{})
	ctx := context.Background()
	lp.Core.SetPaused(ctx, true)
	lp.meter = &quota.Meter{Inspect: func(_ context.Context, provider harness.Provider) (harness.AccountReport, error) {
		return spentReading(provider.Engine), nil
	}}
	lp.SendPMChat(ctx, p.ID, "msg", "hello")
	step(t, lp)
	log, _ := lp.Core.PMChat(ctx, p.ID)
	if log[0].Status != "waiting" {
		t.Fatal(log)
	}
}

func TestPMChatHistoryOnlyCompletedEarlierExchanges(t *testing.T) {
	snap := core.Snapshot{PMChats: []core.PMChatMessage{
		{ID: "first", ProjectID: "p", From: "owner", Text: "Earlier question", Status: "answered"},
		{ID: "failed", ProjectID: "p", From: "owner", Text: "Failed question", Status: "failed"},
		{ID: "current", ProjectID: "p", From: "owner", Text: "Current question", Status: "working"},
		{ID: "later", ProjectID: "p", From: "owner", Text: "Later question", Status: "waiting"},
		{ID: "reply", ProjectID: "p", From: "pm", ReplyTo: "first", Text: "Earlier answer", Status: "answered"},
		{ID: "other", ProjectID: "q", From: "owner", Text: "Other project", Status: "answered"},
	}}
	history := pmChatHistory(snap, "p", "current", 20)
	for _, want := range []string{"Earlier question", "Earlier answer"} {
		if strings.Count(history, want) != 1 {
			t.Fatalf("missing or repeated %q: %s", want, history)
		}
	}
	for _, unwanted := range []string{"Failed question", "Current question", "Later question", "Other project"} {
		if strings.Contains(history, unwanted) {
			t.Fatalf("unexpected %q: %s", unwanted, history)
		}
	}
	p := core.Project{ID: "p", PMDirection: "Owner direction"}
	if strings.Contains(pmToldText(snap, p), "Earlier answer") {
		t.Fatal("chat leaked into landing/escalation directions")
	}
	if !strings.Contains(pmPrompt(snap, p), "Earlier answer") {
		t.Fatal("look forgot completed chat")
	}
}

func TestPMChatThreadHistoryIsSeparateWhileThePMLookSeesProjectPriorities(t *testing.T) {
	var snap core.Snapshot
	for _, conversation := range []string{"", "slack-thread-one", "slack-thread-two"} {
		id := "question-" + conversation
		snap.PMChats = append(snap.PMChats,
			core.PMChatMessage{ID: id, ProjectID: "project", Conversation: conversation, From: "owner", Text: "question in " + id, Status: "answered"},
			core.PMChatMessage{ID: "reply-" + conversation, ProjectID: "project", Conversation: conversation, From: "pm", ReplyTo: id, Text: "reply in " + id, Status: "answered"})
	}
	snap.PMChats = append(snap.PMChats,
		core.PMChatMessage{ID: "other-project", ProjectID: "other", Conversation: "slack-thread-one", From: "owner", Text: "other project text", Status: "answered"},
		core.PMChatMessage{ID: "current", ProjectID: "project", Conversation: "slack-thread-one", From: "owner", Text: "current text", Status: "working"})
	history := pmChatHistory(snap, "project", "current", 20)
	for _, want := range []string{"question in question-slack-thread-one", "reply in question-slack-thread-one"} {
		if !strings.Contains(history, want) {
			t.Errorf("thread omitted %q: %s", want, history)
		}
	}
	for _, unwanted := range []string{"slack-thread-two", "question in question-\n", "other project text", "current text"} {
		if strings.Contains(history, unwanted) {
			t.Errorf("thread included %q: %s", unwanted, history)
		}
	}
	look := pmChatHistory(snap, "project", "", 20)
	for _, want := range []string{"question in question-\n", "slack-thread-one", "slack-thread-two"} {
		if !strings.Contains(look, want) {
			t.Errorf("project overview omitted %q: %s", want, look)
		}
	}
}
