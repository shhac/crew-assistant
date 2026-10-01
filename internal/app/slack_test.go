package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

func slackProject(t *testing.T, a *App, withPM bool) core.Project {
	t.Helper()
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Synthetic Backend", Template: "draft", Brief: core.BriefInput{Goal: "Develop the synthetic product"}})
	if err != nil {
		t.Fatal(err)
	}
	if withPM {
		p, err = a.Core.EditPlaybook(ctx, p.ID, func(_ *core.Snapshot, _ *core.Project, pb *core.Playbook) error {
			pb.Roles = append(pb.Roles, core.Role{Name: "Synthetic PM", Kinds: []string{core.RolePM}, Engine: "claude"})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	a.slackConfig.ProjectID = p.ID
	a.slackConfig.WorkspaceID = "T_SYNTHETIC"
	return p
}

type slackReply struct {
	text string
	err  error
}

func slackAsync(t *testing.T, a *App, m slackapi.Message) <-chan slackReply {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	if claimed, err := a.Core.ClaimEvent(ctx, "slack:"+m.ID); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	replies := make(chan slackReply, 1)
	go func() {
		text, err := a.answerSlack(ctx, m)
		replies <- slackReply{text, err}
	}()
	return replies
}

func slackQueuedPM(t *testing.T, a *App, project, text string) core.PMChatMessage {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		messages, err := a.Core.PMChat(context.Background(), project)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.From == "owner" && m.Text == text {
				return m
			}
		}
	}
	t.Fatalf("PM message %q was never queued", text)
	return core.PMChatMessage{}
}

func slackTakePM(t *testing.T, a *App, project string) (core.PMChatMessage, core.Claim) {
	t.Helper()
	m, claim, _, ok, err := a.Core.ClaimPMChat(context.Background(), project, func(core.Role) string { return "" })
	if err != nil || !ok {
		t.Fatalf("PM claim: %v, %v", ok, err)
	}
	return m, claim
}

func slackResult(t *testing.T, replies <-chan slackReply) slackReply {
	t.Helper()
	select {
	case reply := <-replies:
		return reply
	case <-time.After(3 * time.Second):
		t.Fatal("Slack sender was left waiting")
		return slackReply{}
	}
}

func slackSettled(t *testing.T, a *App, event string, done bool) {
	t.Helper()
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settled, exists := snap.Events["slack:"+event]; !exists || settled != done {
		t.Fatalf("event %q: exists=%v settled=%v, want %v", event, exists, settled, done)
	}
}

func TestSlackProjectMessagesUsePMAndSeparateTransportConversations(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	ctx := context.Background()
	cases := []slackapi.Message{
		{ID: "event-main", Channel: "D_ONE", Text: "First question"},
		{ID: "event-thread", Channel: "D_ONE", ThreadTS: "123.45", Text: "Thread question"},
		{ID: "event-channel", Channel: "D_TWO", Text: "Other channel"},
	}
	var conversations []string
	for _, event := range cases {
		replies := slackAsync(t, a, event)
		queued := slackQueuedPM(t, a, p.ID, event.Text)
		wantConversation := "slack:T_SYNTHETIC:" + event.Channel + ":" + event.ThreadTS
		if queued.Conversation != wantConversation {
			t.Fatalf("conversation %q, want %q", queued.Conversation, wantConversation)
		}
		conversations = append(conversations, queued.Conversation)
		m, claim := slackTakePM(t, a, p.ID)
		if m.ID != queued.ID {
			t.Fatalf("PM took a different message: %+v", m)
		}
		if err := a.Core.AnswerPMChat(ctx, p.ID, m.ID, claim.Token, "Synthetic PM", "Project reply", nil); err != nil {
			t.Fatal(err)
		}
		if err := a.Core.ReleaseProjectClaim(ctx, p.ID, claim.Token); err != nil {
			t.Fatal(err)
		}
		if reply := slackResult(t, replies); reply.err != nil || reply.text != "Project reply" {
			t.Fatalf("Slack reply: %+v", reply)
		}
		slackSettled(t, a, event.ID, true)
		// Re-entering the handler after a lost transport response reuses the
		// deterministic question ID and returns the recorded answer.
		if text, err := a.answerSlack(ctx, event); err != nil || text != "Project reply" {
			t.Fatal(text, err)
		}
		if claimed, err := a.Core.ClaimEvent(ctx, "slack:"+event.ID); err != nil || claimed {
			t.Fatalf("duplicate event reclaimed: %v %v", claimed, err)
		}
	}
	if len(slices.Compact(slices.Clone(conversations))) != 3 {
		t.Fatal("different Slack channels and threads shared a conversation")
	}
	messages, err := a.Core.PMChat(ctx, p.ID)
	if err != nil || len(messages) != 6 {
		t.Fatalf("duplicate questions/replies: %+v %v", messages, err)
	}
	for _, m := range messages {
		if m.From == "pm" && !slices.Contains(conversations, m.Conversation) {
			t.Fatalf("reply lost its conversation: %+v", m)
		}
	}
	turns, err := a.Core.ChatTurns(ctx)
	snap, snapErr := a.Core.Snapshot(ctx)
	if err != nil || snapErr != nil || len(turns) != 0 || len(snap.Messages) != 0 {
		t.Fatalf("project message entered global chat: turns=%+v messages=%+v errors=%v/%v", turns, snap.Messages, err, snapErr)
	}
}

func TestSlackProjectWithoutPMExplainsSetupAndSettlesReceipt(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, false)
	event := slackapi.Message{ID: "no-pm", Channel: "D_SYNTHETIC", Text: "Hello"}
	reply := slackResult(t, slackAsync(t, a, event))
	if reply.err != nil || !strings.Contains(reply.text, "Choose a project manager") || !strings.Contains(reply.text, p.Title+"'s Team page") {
		t.Fatalf("reply: %+v", reply)
	}
	slackSettled(t, a, event.ID, true)
	messages, err := a.Core.PMChat(context.Background(), p.ID)
	if err != nil || len(messages) != 0 {
		t.Fatal(messages, err)
	}
}

func TestSlackProjectMessageDuringStopIsQueuedAndSettlesReceipt(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	a.setStop(lifecycle.Stop{Graceful: stopped, Force: context.Background()})
	event := slackapi.Message{ID: "stopping-pm", Channel: "D_SYNTHETIC", Text: "Queued question"}
	reply := slackResult(t, slackAsync(t, a, event))
	if reply.err != nil || !strings.Contains(reply.text, "queued for the project manager") || !strings.Contains(reply.text, "project's conversation") {
		t.Fatalf("reply: %+v", reply)
	}
	if queued := slackQueuedPM(t, a, p.ID, event.Text); queued.Status != "waiting" {
		t.Fatal(queued)
	}
	slackSettled(t, a, event.ID, true)
}

func TestSlackPMWhenDispatchDisabledAcceptsTwoMessages(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	a.dispatchDisabled.Store(true)
	ctx := context.Background()
	for _, event := range []slackapi.Message{
		{ID: "dispatch-first", Channel: "D_SYNTHETIC", Text: "First paused question"},
		{ID: "dispatch-second", Channel: "D_SYNTHETIC", Text: "Second paused question"},
	} {
		reply := slackResult(t, slackAsync(t, a, event))
		if reply.err != nil || !strings.Contains(reply.text, "queued") || !strings.Contains(reply.text, "dispatch") || !strings.Contains(reply.text, "paused") {
			t.Fatalf("paused dispatch reply: %+v", reply)
		}
		slackSettled(t, a, event.ID, true)
	}
	messages, err := a.Core.PMChat(ctx, p.ID)
	if err != nil || len(messages) != 2 || messages[0].ID == messages[1].ID || messages[0].Status != "waiting" || messages[1].Status != "waiting" {
		t.Fatalf("paused dispatch lost or started a question: %+v %v", messages, err)
	}
	turns, err := a.Core.ChatTurns(ctx)
	snap, snapErr := a.Core.Snapshot(ctx)
	if err != nil || snapErr != nil || len(turns) != 0 || len(snap.Messages) != 0 {
		t.Fatalf("paused PM dispatch entered global chat: turns=%+v messages=%+v errors=%v/%v", turns, snap.Messages, err, snapErr)
	}
}

func TestSlackProjectCancellationKeepsQuestionAndPendingReceipt(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	event := slackapi.Message{ID: "cancel-pm", Channel: "D_SYNTHETIC", Text: "Retain this question"}
	if claimed, err := a.Core.ClaimEvent(ctx, "slack:"+event.ID); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	replies := make(chan slackReply, 1)
	go func() {
		text, err := a.answerSlack(ctx, event)
		replies <- slackReply{text, err}
	}()
	queued := slackQueuedPM(t, a, p.ID, event.Text)
	cancel()
	if reply := slackResult(t, replies); !errors.Is(reply.err, context.Canceled) || reply.text != "" {
		t.Fatalf("reply: %+v", reply)
	}
	slackSettled(t, a, event.ID, false)
	if claimed, err := a.Core.ClaimEvent(context.Background(), "slack:"+event.ID); err != nil || claimed {
		t.Fatalf("uncertain event reclaimed: %v %v", claimed, err)
	}
	messages, err := a.Core.PMChat(context.Background(), p.ID)
	if err != nil || len(messages) != 1 || messages[0].ID != queued.ID || messages[0].Status != "waiting" {
		t.Fatalf("question lost or duplicated: %+v %v", messages, err)
	}
}

func TestSlackPMFailureKeepsRecordedChangesAndGivesInspectableReply(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, true)
	ctx := context.Background()
	event := slackapi.Message{ID: "failed-pm", Channel: "D_SYNTHETIC", Text: "Plan work"}
	replies := slackAsync(t, a, event)
	slackQueuedPM(t, a, p.ID, event.Text)
	m, claim := slackTakePM(t, a, p.ID)
	task, err := a.Core.QueueTaskAs(core.FencedProject(ctx, p.ID, claim.Token), p.ID, core.TaskInput{Objective: "Recorded work"}, "pm")
	if err != nil {
		t.Fatal(err)
	}
	changes := []core.PMChatChange{{Kind: "task.queued", Summary: "Recorded work", Tasks: []string{task.ID}}}
	if err := a.Core.FailPMChat(ctx, p.ID, m.ID, claim.Token, "synthetic private failure", changes); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.ReleaseProjectClaim(ctx, p.ID, claim.Token); err != nil {
		t.Fatal(err)
	}
	if reply := slackResult(t, replies); reply.err != nil || !strings.Contains(reply.text, "Recorded changes were kept") || strings.Contains(reply.text, "synthetic private failure") {
		t.Fatalf("reply: %+v", reply)
	}
	slackSettled(t, a, event.ID, true)
	messages, _ := a.Core.PMChat(ctx, p.ID)
	snap, err := a.Core.Snapshot(ctx)
	if err != nil || len(messages) != 1 || messages[0].Status != "failed" || len(messages[0].Changes) != 1 || !slices.ContainsFunc(snap.Tasks, func(got core.Task) bool { return got.ID == task.ID }) {
		t.Fatalf("lost recorded changes: messages=%+v tasks=%+v error=%v", messages, snap.Tasks, err)
	}
}

func TestSlackSettingsSaveForRestartAndRejectUnknownProject(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, false)
	active := a.slackConfig
	cfg := a.Config()
	cfg.Slack.ProjectID = p.ID
	cfg.Slack.WorkspaceID = "T_SAVED"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if a.Config().Slack != cfg.Slack || a.slackConfig != active {
		t.Fatal("saving changed the active Slack connection or lost the saved selection")
	}
	a.Status("slack", "Slack bot messaging", "connected", "Old connection")
	snap, err := a.Snapshot(context.Background())
	if err != nil || !slices.ContainsFunc(snap.Integrations, func(i core.Integration) bool { return i.ID == "slack" && i.Status == "restart_required" }) {
		t.Fatalf("restart status missing: %+v %v", snap.Integrations, err)
	}
	bad := cfg
	bad.Slack.ProjectID = "missing-project"
	if err := a.UpdateConfig(bad); err == nil || !strings.Contains(err.Error(), "existing project") {
		t.Fatalf("invalid project: %v", err)
	}
	if a.Config().Slack != cfg.Slack || a.slackConfig != active {
		t.Fatal("invalid selection mutated saved or active configuration")
	}
}

func TestSlackProjectNotificationsExcludeOtherProjectsAndAssistantDecisions(t *testing.T) {
	a := testApp(t)
	p := slackProject(t, a, false)
	ctx := context.Background()
	other, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Other synthetic work", Template: "draft", Brief: core.BriefInput{Goal: "Other work"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []core.DecisionInput{
		{ProjectID: p.ID, Title: "Selected project"},
		{ProjectID: other.ID, Title: "Other project"},
		{Title: "Assistant decision"},
	} {
		in.Context, in.Recommendation, in.Choices = "Synthetic choice", "Continue", []string{"Continue", "Stop"}
		if _, err := a.Core.CreateDecision(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	var sent []string
	send := func(_ context.Context, text string) error { sent = append(sent, text); return nil }
	a.notifyProject(ctx, p.ID, send)
	a.notifyProject(ctx, p.ID, send)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Selected project\n") {
		t.Fatalf("unscoped or duplicate notifications: %+v", sent)
	}
	// Excluded decisions remain available for another connection; filtering
	// must happen before the outbound operation is claimed.
	a.notifyProject(ctx, other.ID, send)
	if len(sent) != 2 || !strings.HasPrefix(sent[1], "Other project\n") {
		t.Fatalf("excluded decision was consumed: %+v", sent)
	}
}
