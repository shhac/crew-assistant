package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

// summaries stands in for the assistant's model when it summarizes: it
// answers every request with summary and keeps the dialogue each one folded
// in.
type summaries struct {
	mu     sync.Mutex
	folded [][]string
	// failFrom, when set, is the request from which the model fails.
	failFrom int
}

func (s *summaries) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.folded)
}

// last is the dialogue the latest request folded in.
func (s *summaries) last() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.folded[len(s.folded)-1]
}

func summaryModel(t *testing.T, a *App, summary string) *summaries {
	t.Helper()
	s := &summaries{}
	a.summarize = func(_ context.Context, _ engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
		if len(m) != 2 || !strings.HasPrefix(m[0].Content, "Summarize this past conversation") || len(tools) != 0 {
			t.Errorf("not a summary request: %+v", m)
		}
		if strings.Contains(m[1].Content, "/compact") {
			t.Error("the command was given to the model")
		}
		var payload struct {
			Dialogue []engine.Message `json:"dialogue"`
		}
		if err := json.Unmarshal([]byte(m[1].Content), &payload); err != nil {
			t.Error(err)
		}
		said := []string{}
		for _, d := range payload.Dialogue {
			said = append(said, d.Content)
		}
		s.mu.Lock()
		s.folded = append(s.folded, said)
		failing := s.failFrom > 0 && len(s.folded) >= s.failFrom
		n := len(s.folded)
		s.mu.Unlock()
		if failing {
			return engine.Message{}, engine.Usage{}, errors.New("provider unavailable")
		}
		return engine.Message{Role: "assistant", Content: fmt.Sprintf("%s (%d)", summary, n)}, engine.Usage{}, nil
	}
	return s
}

// waitFor waits for the state to satisfy ok.
func waitFor(t *testing.T, a *App, ok func(core.Snapshot) bool) core.Snapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := a.Core.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if ok(snap) {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("state never got there")
	return core.Snapshot{}
}

func turnDone(id string) func(core.Snapshot) bool {
	return func(s core.Snapshot) bool {
		for _, t := range s.ChatTurns {
			if t.ID == id && t.Status != "queued" && t.Status != "running" {
				return true
			}
		}
		return false
	}
}

// recordingModel answers each message and keeps what it was asked.
func recordingModel(a *App) func() []engine.Request {
	var mu sync.Mutex
	var seen []engine.Request
	a.chatInvoker = func(_ context.Context, _ engine.Config, req engine.Request, _ engine.ToolExecutor) (engine.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, req)
		return engine.Result{Message: "Reply to " + req.Message}, nil
	}
	return func() []engine.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]engine.Request(nil), seen...)
	}
}

func TestCompactSummarizesAndTheAssistantGoesOnFromTheSummaryAndRecentTurns(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := summaryModel(t, a, "The owner is planning a fictional garden.")
	seen := recordingModel(a)
	for i := 0; i < 6; i++ {
		settledReply(t, a, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i), fmt.Sprintf("reply %d", i))
	}
	before, _ := a.Core.Snapshot(ctx)
	startTestQueue(t, a)
	if _, err := a.EnqueueChat(ctx, "compact", "/compact"); err != nil {
		t.Fatal(err)
	}
	snap := waitFor(t, a, turnDone("compact"))
	if len(seen()) != 0 {
		t.Fatal("the command was sent to the model as a message", seen())
	}
	if calls.calls() != 1 || snap.ChatCheckpoint.ThroughID != before.Messages[7].ID || len(calls.last()) != 8 {
		t.Fatal("did not summarize all but the last two exchanges", calls.calls(), snap.ChatCheckpoint)
	}
	last := snap.Messages[len(snap.Messages)-1]
	if len(snap.Messages) != 13 || last.Role != core.RoleSummary || last.Content != "The owner is planning a fictional garden. (1)" {
		t.Fatal("the summary is not shown", snap.Messages)
	}
	turns, _ := a.Core.ChatTurns(ctx)
	command := turns[len(turns)-1]
	if command.Status != "completed" || command.UserMessageID != "" || command.Outcome != "Summarized 8 earlier messages. The latest exchanges carry on word for word." {
		t.Fatal(command)
	}
	if _, err := a.EnqueueChat(ctx, "next", "What next?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, turnDone("next"))
	req := seen()[0]
	if len(req.History) != 4 || req.History[0].Content != "message 4" || req.History[3].Content != "reply 5" {
		t.Fatal("the assistant did not go on from the recent turns", req.History)
	}
	if !strings.Contains(string(req.Context), "The owner is planning a fictional garden.") || strings.Contains(string(req.Context), "message 0") {
		t.Fatal("the assistant did not go on from the summary")
	}
}

// compactNow runs /compact through the queue and returns its turn.
func compactNow(t *testing.T, a *App, id string) core.ChatTurn {
	t.Helper()
	if _, err := a.EnqueueChat(context.Background(), id, "/compact"); err != nil {
		t.Fatal(err)
	}
	snap := waitFor(t, a, turnDone(id))
	for _, turn := range snap.ChatTurns {
		if turn.ID == id {
			return turn
		}
	}
	return core.ChatTurn{}
}

func TestCompactingAgainStraightAwayHasNothingToFoldIn(t *testing.T) {
	a := testApp(t)
	calls := summaryModel(t, a, "A fictional summary.")
	recordingModel(a)
	for i := 0; i < 6; i++ {
		settledReply(t, a, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i), fmt.Sprintf("reply %d", i))
	}
	startTestQueue(t, a)
	compactNow(t, a, "first")
	again := compactNow(t, a, "again")
	if again.Status != "completed" || !strings.HasPrefix(again.Outcome, "Nothing to compact yet") || calls.calls() != 1 {
		t.Fatal(again, calls.calls())
	}
	// Nothing new is shown either: only the first summary is in the thread.
	snap, _ := a.Core.Snapshot(context.Background())
	shown := 0
	for _, m := range snap.Messages {
		if m.Role == core.RoleSummary {
			shown++
		}
	}
	if shown != 1 {
		t.Fatal(shown)
	}
}

// Each /compact after more has been said folds in only what fell out of the
// last two exchanges; the summaries shown in the thread never take a kept
// exchange's place.
func TestRepeatedCompactionKeepsTheLatestExchangesWordForWord(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := summaryModel(t, a, "A fictional summary.")
	seen := recordingModel(a)
	for i := 0; i < 3; i++ {
		settledReply(t, a, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d", i), fmt.Sprintf("reply %d", i))
	}
	startTestQueue(t, a)
	compactNow(t, a, "compact-0")
	if want := []string{"message 0", "reply 0"}; !equalStrings(calls.last(), want) {
		t.Fatal(calls.last())
	}
	// What each later /compact should fold in: the exchange that fell out of
	// the window, and nothing else.
	falls := [][]string{
		{"message 1", "reply 1"},
		{"message 2", "reply 2"},
		{"topic 1", "Reply to topic 1"},
	}
	for round := 1; round <= 3; round++ {
		id := fmt.Sprintf("next-%d", round)
		if _, err := a.EnqueueChat(ctx, id, fmt.Sprintf("topic %d", round)); err != nil {
			t.Fatal(err)
		}
		waitFor(t, a, turnDone(id))
		compacted := compactNow(t, a, fmt.Sprintf("compact-%d", round))
		if compacted.Status != "completed" || calls.calls() != round+1 {
			t.Fatal(round, compacted, calls.calls())
		}
		if !equalStrings(calls.last(), falls[round-1]) {
			t.Fatal(round, calls.last())
		}
	}
	// The assistant goes on from the summary and the last two exchanges.
	if _, err := a.EnqueueChat(ctx, "last", "And now?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, turnDone("last"))
	got := seen()
	history := []string{}
	for _, m := range got[len(got)-1].History {
		history = append(history, m.Content)
	}
	if want := []string{"topic 2", "Reply to topic 2", "topic 3", "Reply to topic 3"}; !equalStrings(history, want) {
		t.Fatal(history)
	}
}

// longExchanges records n exchanges long enough that folding them in takes
// more than one summary request.
func longExchanges(t *testing.T, a *App, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		settledReply(t, a, fmt.Sprintf("m%d", i), fmt.Sprintf("message %d %s", i, strings.Repeat("x", 10_000)), fmt.Sprintf("reply %d %s", i, strings.Repeat("y", 10_000)))
	}
}

// Long dialogue is summarized in batches, each saved as it is made. When a
// later batch fails, the assistant goes on from what was saved, so the owner
// sees that summary and is told compaction stopped part way.
func TestACompactionThatStopsPartWayShowsTheSummaryInForce(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := summaryModel(t, a, "Partial summary.")
	calls.failFrom = 2
	seen := recordingModel(a)
	longExchanges(t, a, 6)
	before, _ := a.Core.Snapshot(ctx)
	startTestQueue(t, a)
	turn := compactNow(t, a, "compact")
	if calls.calls() != 2 {
		t.Fatalf("expected a saved batch and then a failed one, got %d requests", calls.calls())
	}
	if turn.Status != "completed" || !strings.HasPrefix(turn.Outcome, "Summarized 4 earlier messages, then stopped before the rest.") {
		t.Fatal(turn)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.ChatCheckpoint.ThroughID != before.Messages[3].ID {
		t.Fatal("the first batch was not saved", snap.ChatCheckpoint)
	}
	last := snap.Messages[len(snap.Messages)-1]
	if last.Role != core.RoleSummary || last.Content != snap.ChatCheckpoint.Summary || last.Content != "Partial summary. (1)" || turn.AssistantMessageID != last.ID {
		t.Fatal("the summary in force is not shown", last)
	}
	// What the assistant goes on from is what the owner was shown: that
	// summary, and every message after it word for word.
	if _, err := a.EnqueueChat(ctx, "next", "What next?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, a, turnDone("next"))
	req := seen()[0]
	if len(req.History) != 8 || !strings.HasPrefix(req.History[0].Content, "message 2 ") || !strings.Contains(string(req.Context), "Partial summary. (1)") {
		t.Fatal(len(req.History), req.History[0].Content[:12])
	}
}

// A compaction that saves nothing changes nothing, and says it failed.
func TestACompactionThatSavesNothingFailsAndShowsNoSummary(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	calls := summaryModel(t, a, "Never saved.")
	calls.failFrom = 1
	longExchanges(t, a, 6)
	startTestQueue(t, a)
	turn := compactNow(t, a, "compact")
	if turn.Status != "failed" || turn.Error == "" || turn.AssistantMessageID != "" {
		t.Fatal(turn)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.ChatCheckpoint.ThroughID != "" || snap.Messages[len(snap.Messages)-1].Role == core.RoleSummary {
		t.Fatal("a failed compaction changed the conversation", snap.ChatCheckpoint)
	}
}

// A summary in the thread is not dialogue: it never counts toward what is
// kept word for word.
func TestSummariesInTheThreadDoNotTakeAKeptMessagesPlace(t *testing.T) {
	said := func(id, role string) core.Message { return core.Message{ID: id, Role: role, Content: id} }
	messages := []core.Message{
		said("u0", "user"), said("a0", "assistant"),
		said("u1", "user"), said("a1", "assistant"),
		said("u2", "user"), said("a2", "assistant"),
		said("s1", core.RoleSummary), said("s2", core.RoleSummary),
	}
	source, through, err := chatSummaryBatchKeeping(messages, 0, "", 4, 1)
	if err != nil || through != "a0" || len(source) != 2 || source[0].Content != "u0" {
		t.Fatal(source, through, err)
	}
	// With no more than the kept exchanges said, there is nothing to fold in.
	if _, through, _ := chatSummaryBatchKeeping(messages[2:], 0, "", 4, 1); through != "" {
		t.Fatal(through)
	}
}

func TestNewAndClearStartFreshWithoutHistory(t *testing.T) {
	for _, command := range []string{"/new", "/clear"} {
		t.Run(command, func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			seen := recordingModel(a)
			settledReply(t, a, "old", "An old topic", "An old reply")
			startTestQueue(t, a)
			if _, err := a.EnqueueChat(ctx, "fresh", command); err != nil {
				t.Fatal(err)
			}
			waitFor(t, a, turnDone("fresh"))
			if _, err := a.EnqueueChat(ctx, "next", "Hello again"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, a, turnDone("next"))
			got := seen()
			if len(got) != 1 || got[0].Message != "Hello again" {
				t.Fatal("the command reached the model", got)
			}
			if h := got[0].History; len(h) != 1 || !strings.HasPrefix(h[0].Content, "Fresh start.") {
				t.Fatal("the fresh conversation carries history", h)
			}
			if strings.Contains(string(got[0].Context), "An old") {
				t.Fatal("the old conversation reached the model")
			}
			list, _ := a.Core.Conversations(ctx)
			if len(list) != 1 || list[0].Title != "An old topic" {
				t.Fatal("the old conversation was not archived", list)
			}
		})
	}
}

func TestTheAssistantCanStartItsOwnConversationAfreshAfterItsReply(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	var result any
	var toolErr error
	a.chatInvoker = func(ctx context.Context, cfg engine.Config, req engine.Request, executor engine.ToolExecutor) (engine.Result, error) {
		if req.Message == "Let's start over" {
			result, toolErr = executor.Execute(ctx, "manage_conversation", json.RawMessage(`{"whose":"assistant","action":"new","project_id":"","task_id":""}`))
			return engine.Result{Message: "Starting afresh."}, nil
		}
		return engine.Result{Message: "ok"}, nil
	}
	startTestQueue(t, a)
	if _, err := a.EnqueueChat(ctx, "ask", "Let's start over"); err != nil {
		t.Fatal(err)
	}
	snap := waitFor(t, a, func(s core.Snapshot) bool { return len(s.Conversations) == 1 })
	if toolErr != nil || !strings.Contains(fmt.Sprint(result), "Queued") {
		t.Fatal(result, toolErr)
	}
	// The reply stays with the conversation it answered; the fresh one opens
	// with the overview only.
	old := snap.Conversations[0].Messages
	if len(old) != 2 || old[1].Content != "Starting afresh." {
		t.Fatal(old)
	}
	if len(snap.Messages) != 1 || snap.Messages[0].Origin != core.OriginOverview {
		t.Fatal(snap.Messages)
	}
	for _, turn := range snap.ChatTurns {
		if turn.Command == core.CommandNew && turn.Origin != core.OriginAssistant {
			t.Fatal(turn)
		}
	}
	if _, err := a.Execute(ctx, "manage_conversation", json.RawMessage(`{"whose":"reviewer","action":"new","project_id":"","task_id":""}`)); err == nil {
		t.Fatal("a conversation that does not exist was accepted")
	}
	if _, err := a.Execute(ctx, "manage_conversation", json.RawMessage(`{"whose":"assistant","action":"delete","project_id":"","task_id":""}`)); err == nil {
		t.Fatal("an unknown action was accepted")
	}
}

func TestTheAssistantCanTellATaskImplementerToCompactOrStartAfresh(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Fictional notes", Brief: core.BriefInput{Goal: "A fictional note"}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Draft it"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskReviewing
		t.Roles = []core.Role{{Name: "Writer", Kinds: []string{core.RoleImplementer}, Engine: "claude"}}
		t.WriterSession = []byte(`{"engine":"claude","id":"writer"}`)
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	call := func(action string) any {
		t.Helper()
		out, err := a.Execute(ctx, "manage_conversation", json.RawMessage(fmt.Sprintf(`{"whose":"implementer","action":%q,"project_id":%q,"task_id":%q}`, action, p.ID, task.ID)))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The model is told why, so it can offer the alternative.
	declined, _ := call("compact").(map[string]string)
	if !strings.Contains(declined["declined"], "start its conversation afresh instead") || strings.Contains(declined["declined"], "state conflict") {
		t.Fatal(declined)
	}
	if out, _ := call("new").(map[string]string); out["status"] == "" {
		t.Fatal(out)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Tasks[0].WriterNext != core.WriterFresh {
		t.Fatal(snap.Tasks[0])
	}
}

func TestASuggestionIsNeverACommand(t *testing.T) {
	a, f := suggestionApp(t, "codex")
	for _, reply := range []string{"/new", "/compact", "/whatever"} {
		f.reply = reply
		after := settledReply(t, a, "s-"+strings.TrimPrefix(reply, "/"), "Hello", "Hi there.")
		if got, err := a.SuggestNextMessage(context.Background(), after); err != nil || got != "" {
			t.Fatal(reply, got, err)
		}
	}
}

func TestAFreshConversationStillGetsSuggestions(t *testing.T) {
	a, _ := suggestionApp(t, "codex")
	ctx := context.Background()
	settledReply(t, a, "old", "Hello", "Hi there.")
	if _, err := a.Core.EnqueueChat(ctx, "new", "/new"); err != nil {
		t.Fatal(err)
	}
	turn, _ := a.Core.StartNextChat(ctx)
	if err := a.Core.FinishChatCommand(ctx, turn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if got, err := a.SuggestNextMessage(ctx, snap.Messages[0].ID); err != nil || got != "What should I plant first?" {
		t.Fatal(got, err)
	}
}
