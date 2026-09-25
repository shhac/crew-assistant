package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestChatCommandsAreWholeMessagesOnly(t *testing.T) {
	for message, want := range map[string]string{
		"/compact":        CommandCompact,
		"  /NEW\n":        CommandNew,
		"/clear":          CommandClear,
		"/new plan":       "",
		"please /compact": "",
		"/usr/bin":        "",
		"/":               "",
		"compact":         "",
	} {
		got, err := ChatCommand(message)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", message, got, err, want)
		}
	}
	if _, err := ChatCommand("/summarize"); !errors.Is(err, ErrChatValidation) || !strings.Contains(err.Error(), "/summarize isn't a command") {
		t.Fatal(err)
	}
}

func TestAnUnknownCommandIsRefusedAndAKnownOneNeverReachesTheConversation(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	if _, err := s.EnqueueChat(ctx, "typo", "/compcat"); !errors.Is(err, ErrChatValidation) {
		t.Fatal(err)
	}
	if turns, _ := s.ChatTurns(ctx); len(turns) != 0 {
		t.Fatal("an unknown command was queued", turns)
	}
	turn, err := s.EnqueueChat(ctx, "compact", "/compact")
	if err != nil || turn.Command != CommandCompact {
		t.Fatal(turn, err)
	}
	started, err := s.StartNextChat(ctx)
	if err != nil || started.Command != CommandCompact || started.UserMessageID != "" {
		t.Fatal(started, err)
	}
	if err := s.FinishChatCommand(ctx, started.ID, "", "Nothing to compact yet."); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(ctx)
	if len(snap.Messages) != 0 {
		t.Fatal("the command entered the conversation", snap.Messages)
	}
	turns, _ := s.ChatTurns(ctx)
	if len(turns) != 1 || turns[0].Status != "completed" || turns[0].Outcome != "Nothing to compact yet." {
		t.Fatal(turns)
	}
	// Editing a queued message into a command makes it one, and back.
	queued, _ := s.EnqueueChat(ctx, "edited", "Hello")
	edited, err := s.EditChatMessage(ctx, queued.ID, "/new", queued.Revision)
	if err != nil || edited.Command != CommandNew {
		t.Fatal(edited, err)
	}
	if _, err := s.EditChatMessage(ctx, queued.ID, "/nwe", edited.Revision); !errors.Is(err, ErrChatValidation) {
		t.Fatal(err)
	}
	edited, err = s.EditChatMessage(ctx, queued.ID, "Hello again", edited.Revision)
	if err != nil || edited.Command != "" {
		t.Fatal(edited, err)
	}
}

// exchange runs one owner message and reply through the queue.
func exchange(t *testing.T, s *Service, id, message, reply string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.EnqueueChat(ctx, id, message); err != nil {
		t.Fatal(err)
	}
	turn, err := s.StartNextChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChat(ctx, turn.ID, "completed", reply, ""); err != nil {
		t.Fatal(err)
	}
}

func TestANewConversationArchivesTheOldOneAndOpensWithAnOverview(t *testing.T) {
	for _, command := range []string{"/new", "/clear"} {
		t.Run(command, func(t *testing.T) {
			s, _ := fixture(t)
			ctx := context.Background()
			p := newProject(t, s)
			running, err := s.QueueTask(ctx, p.ID, TaskInput{Objective: "Draft the fictional launch note"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateTask(ctx, running.ID, func(t *Task, _ *Project) (string, error) {
				t.Status = TaskWriting
				return "", nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.QueueTask(ctx, p.ID, TaskInput{Objective: "Proofread it"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateDecision(ctx, DecisionInput{ProjectID: p.ID, Title: "Pick a fictional audience", Context: "Who reads it", Recommendation: "A", Choices: []string{"A", "B"}}); err != nil {
				t.Fatal(err)
			}
			exchange(t, s, "one", "Plan the  fictional\nlaunch", "Planned.")
			if err := s.SaveChatCheckpoint(ctx, "", mustSnapshot(t, s).Messages[1].ID, "earlier summary"); err != nil {
				t.Fatal(err)
			}
			old := mustSnapshot(t, s)
			if _, err := s.EnqueueChat(ctx, "fresh", command); err != nil {
				t.Fatal(err)
			}
			turn, err := s.StartNextChat(ctx)
			if err != nil || turn.Command == "" {
				t.Fatal(turn, err)
			}
			if err := s.FinishChatCommand(ctx, turn.ID, "", "Started afresh."); err != nil {
				t.Fatal(err)
			}
			snap := mustSnapshot(t, s)
			if len(snap.Messages) != 1 || snap.Messages[0].Role != "assistant" || snap.Messages[0].Origin != OriginOverview {
				t.Fatal("the fresh conversation carries history", snap.Messages)
			}
			overview := snap.Messages[0].Content
			for _, want := range []string{"Fictional project", "Draft the fictional launch note (Fictional project): writing", "1 more waiting to start", "Pick a fictional audience (Fictional project)", "saved in History"} {
				if !strings.Contains(overview, want) {
					t.Errorf("overview lacks %q:\n%s", want, overview)
				}
			}
			if strings.Contains(overview, "Plan the") || snap.ChatCheckpoint.Summary != "" {
				t.Fatal("the old conversation leaked into the new one")
			}
			if turns, _ := s.ChatTurns(ctx); len(turns) != 0 {
				t.Fatal("the old conversation's turns are still shown", turns)
			}
			list, _ := s.Conversations(ctx)
			if len(list) != 1 || list[0].Title != "Plan the fictional launch" || list[0].Messages != 2 {
				t.Fatal(list)
			}
			archived, err := s.Conversation(ctx, list[0].ID)
			if err != nil || len(archived.Messages) != 2 || archived.Messages[0].ID != old.Messages[0].ID || archived.Checkpoint.Summary != "earlier summary" {
				t.Fatal("the archive is not the whole conversation", archived, err)
			}
		})
	}
}

func TestSwitchingBackPicksUpTheArchivedConversationWhereItWasLeft(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	exchange(t, s, "one", "First topic", "On the first topic.")
	first := mustSnapshot(t, s)
	if _, err := s.EnqueueChat(ctx, "new", "/new"); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.StartNextChat(ctx)
	if err := s.FinishChatCommand(ctx, turn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	exchange(t, s, "two", "Second topic", "On the second topic.")
	list, _ := s.Conversations(ctx)
	if len(list) != 1 {
		t.Fatal(list)
	}
	// Nothing switches while a reply is under way: it belongs where it began.
	if _, err := s.EnqueueChat(ctx, "busy", "Still going"); err != nil {
		t.Fatal(err)
	}
	busy, _ := s.StartNextChat(ctx)
	if err := s.ResumeConversation(ctx, list[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := s.FinishChat(ctx, busy.ID, "completed", "Done.", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ResumeConversation(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.ResumeConversation(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	snap := mustSnapshot(t, s)
	if len(snap.Messages) != 2 || snap.Messages[0].ID != first.Messages[0].ID || snap.ConversationID != list[0].ID {
		t.Fatal("the first conversation was not picked up", snap.Messages)
	}
	turns, _ := s.ChatTurns(ctx)
	// Its turns come back, ending with the /new that closed it.
	if len(turns) != 2 || turns[0].ID != "one" || turns[1].ID != "new" {
		t.Fatal("the first conversation's turns are not back", turns)
	}
	// The one it replaced is archived in turn, so the owner can come back.
	list, _ = s.Conversations(ctx)
	if len(list) != 1 || list[0].Title != "Second topic" || list[0].Messages != 5 {
		t.Fatal(list)
	}
	// Carrying on adds to the conversation picked up.
	exchange(t, s, "three", "Back to the first", "Carrying on.")
	if snap := mustSnapshot(t, s); len(snap.Messages) != 4 {
		t.Fatal(snap.Messages)
	}
}

// The queue view is one reading: its turns always belong to the conversation
// it names, before and after a reset.
func TestTheQueueNamesTheConversationItsTurnsBelongTo(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	exchange(t, s, "one", "First topic", "Reply.")
	before, err := s.ChatQueue(ctx)
	if err != nil || len(before.Turns) != 1 || before.Turns[0].Conversation != before.Conversation {
		t.Fatal(before, err)
	}
	if _, err := s.EnqueueChat(ctx, "new", "/new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChat(ctx, "waiting", "Next"); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.StartNextChat(ctx)
	if err := s.FinishChatCommand(ctx, turn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	after, err := s.ChatQueue(ctx)
	if err != nil || after.Conversation == before.Conversation {
		t.Fatal(after, err)
	}
	// Only the message still waiting shows; it joins whichever conversation
	// is current when it starts.
	if len(after.Turns) != 1 || after.Turns[0].ID != "waiting" || after.Turns[0].Status != "queued" {
		t.Fatal(after.Turns)
	}
}

// A command waiting in the queue was meant for the conversation it was asked
// in. Switching first would have it reset the conversation picked up instead,
// so switching waits until nothing is waiting, held or not.
func TestSwitchingWaitsForEverythingQueuedInTheCurrentConversation(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	exchange(t, s, "a", "Topic A", "On A.")
	if _, err := s.EnqueueChat(ctx, "to-b", "/new"); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.StartNextChat(ctx)
	if err := s.FinishChatCommand(ctx, turn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	exchange(t, s, "b", "Topic B", "On B.")
	convA, _ := s.Conversations(ctx)
	convB := mustSnapshot(t, s).ConversationID
	// The assistant asks to start its own conversation afresh during a
	// reply, and the owner is editing the queue so the command is held.
	if _, err := s.EnqueueChat(ctx, "ask", "Start over"); err != nil {
		t.Fatal(err)
	}
	busy, _ := s.StartNextChat(ctx)
	asked, err := s.QueueChatCommand(ctx, CommandNew)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishChat(ctx, busy.ID, "completed", "Starting over.", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HoldChat(ctx, asked.ID, "editing", MaxChatHold); err != nil {
		t.Fatal(err)
	}
	err = s.ResumeConversation(ctx, convA[0].ID)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "waiting to run") {
		t.Fatal("switched past a held command", err)
	}
	if mustSnapshot(t, s).ConversationID != convB {
		t.Fatal("the refused switch changed the conversation")
	}
	// Once released, the command resets its own conversation, B.
	if err := s.ReleaseChatHold(ctx, asked.ID); err != nil {
		t.Fatal(err)
	}
	started, err := s.StartNextChat(ctx)
	if err != nil || started.ID != asked.ID || started.Conversation != convB {
		t.Fatal(started, err)
	}
	if err := s.FinishChatCommand(ctx, started.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Conversations(ctx)
	titles := []string{}
	for _, c := range list {
		titles = append(titles, c.Title)
	}
	if len(list) != 2 || !slices.Contains(titles, "Topic A") || !slices.Contains(titles, "Topic B") {
		t.Fatal("B was not the one archived", titles)
	}
	// Now nothing waits, A can be picked up, whole.
	if err := s.ResumeConversation(ctx, convA[0].ID); err != nil {
		t.Fatal(err)
	}
	if snap := mustSnapshot(t, s); len(snap.Messages) != 2 || snap.Messages[0].Content != "Topic A" {
		t.Fatal(snap.Messages)
	}
	// An ordinary message waiting to run holds the switch too.
	if _, err := s.EnqueueChat(ctx, "waiting", "Later"); err != nil {
		t.Fatal(err)
	}
	var bID string
	for _, c := range mustConversations(t, s) {
		if c.Title == "Topic B" {
			bID = c.ID
		}
	}
	if err := s.ResumeConversation(ctx, bID); !errors.Is(err, ErrConflict) {
		t.Fatal("switched past a waiting message", err)
	}
}

func mustConversations(t *testing.T, s *Service) []ConversationEntry {
	t.Helper()
	list, err := s.Conversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestACompactionIsShownWhereItWasMade(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	exchange(t, s, "one", "Hello", "Hi.")
	if _, err := s.EnqueueChat(ctx, "compact", "/compact"); err != nil {
		t.Fatal(err)
	}
	turn, _ := s.StartNextChat(ctx)
	if err := s.FinishChatCommand(ctx, turn.ID, "The owner said hello.", "Summarized 2 earlier messages."); err != nil {
		t.Fatal(err)
	}
	snap := mustSnapshot(t, s)
	last := snap.Messages[len(snap.Messages)-1]
	if len(snap.Messages) != 3 || last.Role != RoleSummary || last.Content != "The owner said hello." {
		t.Fatal(snap.Messages)
	}
	turns, _ := s.ChatTurns(ctx)
	if turns[1].AssistantMessageID != last.ID || turns[1].Outcome != "Summarized 2 earlier messages." {
		t.Fatal(turns[1])
	}
	if err := s.FinishChatCommand(ctx, turn.ID, "again", ""); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestACommandTheAssistantAsksForRunsNextAndOnce(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	if _, err := s.EnqueueChat(ctx, "running", "Tidy up"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartNextChat(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChat(ctx, "later", "Something else"); err != nil {
		t.Fatal(err)
	}
	asked, err := s.QueueChatCommand(ctx, CommandNew)
	if err != nil || asked.Origin != OriginAssistant || asked.Message != "/new" {
		t.Fatal(asked, err)
	}
	again, _ := s.QueueChatCommand(ctx, CommandNew)
	if again.ID != asked.ID {
		t.Fatal("asked twice, queued twice")
	}
	if _, err := s.QueueChatCommand(ctx, "shell"); !errors.Is(err, ErrChatValidation) {
		t.Fatal(err)
	}
	turns, _ := s.ChatTurns(ctx)
	if len(turns) != 3 || turns[1].ID != asked.ID || turns[2].ID != "later" {
		t.Fatal("the command does not run next", turns)
	}
}

func TestOnlyAnImplementerWithAConversationCanBeToldToCompactOrStartAfresh(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := newProject(t, s)
	task, err := s.QueueTask(ctx, p.ID, TaskInput{Objective: "Draft the fictional note"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterFresh); !errors.Is(err, ErrConflict) {
		t.Fatal("no conversation yet, but accepted", err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskReviewing
		t.Roles = []Role{{Name: "Writer", Kinds: []string{RoleImplementer}, Engine: "claude"}}
		t.WriterSession = []byte(`{"engine":"claude","id":"writer"}`)
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWriterNext(ctx, "another-project", task.ID, WriterFresh); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterCompact); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "start its conversation afresh instead") {
		t.Fatal("a Claude implementer was compacted", err)
	}
	fresh, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterFresh)
	if err != nil || fresh.WriterNext != WriterFresh {
		t.Fatal(fresh, err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Roles[0].Engine = "codex"
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	compacted, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterCompact)
	if err != nil || compacted.WriterNext != WriterCompact {
		t.Fatal(compacted, err)
	}
	// Each request is its own, even one asking for the same thing again.
	again, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterCompact)
	if err != nil || fresh.WriterRequest != 1 || compacted.WriterRequest != 2 || again.WriterRequest != 3 {
		t.Fatal(fresh.WriterRequest, compacted.WriterRequest, again.WriterRequest, err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskStopped
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWriterNext(ctx, p.ID, task.ID, WriterFresh); !errors.Is(err, ErrConflict) {
		t.Fatal("a finished task was changed", err)
	}
}

func mustSnapshot(t *testing.T, s *Service) Snapshot {
	t.Helper()
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
