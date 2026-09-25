package core

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// The owner's chat commands. A command is a whole message on its own, such as
// "/compact": it runs in the queue in its turn, like any message, but never
// enters the conversation or reaches the model.
const (
	// CommandCompact summarizes the conversation so far; the assistant goes on
	// from the summary and the most recent exchanges.
	CommandCompact = "compact"
	// CommandNew and CommandClear archive the conversation and start a fresh
	// one, opening with an overview of what is going on.
	CommandNew   = "new"
	CommandClear = "clear"
)

var commandSyntax = regexp.MustCompile(`^/([A-Za-z][A-Za-z0-9_-]*)$`)

// ChatCommand reports the command a message is, or "" for an ordinary
// message. Only the whole message counts: "/new" is a command, and "/new
// plan" or a path such as "/usr/bin" is not. A command nobody knows is
// refused, so it is never sent to the model by mistake.
func ChatCommand(message string) (string, error) {
	m := commandSyntax.FindStringSubmatch(strings.TrimSpace(message))
	if m == nil {
		return "", nil
	}
	switch name := strings.ToLower(m[1]); name {
	case CommandCompact, CommandNew, CommandClear:
		return name, nil
	default:
		return "", fmt.Errorf("/%s isn't a command. Try /compact to summarize the conversation, or /new or /clear to start a fresh one: %w", m[1], ErrChatValidation)
	}
}

// OriginAssistant marks a command turn the assistant asked for itself.
const OriginAssistant = "assistant"

// Message roles the owner sees in the conversation that are not dialogue: the
// model is never given them as something said.
const (
	// RoleSummary is the summary /compact made, shown where it was made.
	RoleSummary = "summary"
)

// OriginOverview marks the assistant message a fresh conversation opens with.
const OriginOverview = "overview"

// Conversation is an archived conversation with the assistant, kept whole with
// the summary it was going on from, so the owner can read it or pick it up
// again.
type Conversation struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	StartedAt  time.Time      `json:"started_at"`
	ArchivedAt time.Time      `json:"archived_at"`
	Messages   []Message      `json:"messages"`
	Checkpoint ChatCheckpoint `json:"checkpoint"`
	// Session is the model session it ran on, resumed if it is picked up.
	Session *ChatSession `json:"session,omitempty"`
}

// ConversationEntry lists an archived conversation without its messages.
type ConversationEntry struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	StartedAt  time.Time `json:"started_at"`
	ArchivedAt time.Time `json:"archived_at"`
	Messages   int       `json:"messages"`
}

// Conversations lists archived conversations, most recently archived first.
func (s *Service) Conversations(ctx context.Context) ([]ConversationEntry, error) {
	v, err := s.store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	out := []ConversationEntry{}
	for _, c := range v.Conversations {
		out = append(out, ConversationEntry{ID: c.ID, Title: c.Title, StartedAt: c.StartedAt, ArchivedAt: c.ArchivedAt, Messages: len(c.Messages)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ArchivedAt.After(out[j].ArchivedAt) })
	return out, nil
}

// Conversation is one archived conversation, messages and all.
func (s *Service) Conversation(ctx context.Context, id string) (Conversation, error) {
	v, err := s.store.Snapshot(ctx)
	if err != nil {
		return Conversation{}, err
	}
	for _, c := range v.Conversations {
		if c.ID == id {
			c.Session = c.Session.shown()
			return c, nil
		}
	}
	return Conversation{}, ErrNotFound
}

// ResumeConversation archives the current conversation and picks an archived
// one up again where it was left. It waits for no one, so it refuses while
// anything is still to happen in the current conversation: a reply under way
// belongs where it started, and a message, wake-up or command waiting to run
// (held while it is edited or not) was meant for this conversation. A /new
// the assistant asked for must reset its own conversation, not the one picked
// up in its place.
func (s *Service) ResumeConversation(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		for _, t := range v.ChatTurns {
			switch t.Status {
			case "running":
				return fmt.Errorf("the assistant is replying; switch once it has finished: %w", ErrConflict)
			case "queued":
				return fmt.Errorf("messages are still waiting to run in this conversation; switch once they have, or cancel them: %w", ErrConflict)
			}
		}
		at := -1
		for i, c := range v.Conversations {
			if c.ID == id {
				at = i
			}
		}
		if at < 0 {
			return ErrNotFound
		}
		picked := v.Conversations[at]
		v.Conversations = append(v.Conversations[:at], v.Conversations[at+1:]...)
		archiveConversation(v, s.now().UTC())
		v.ConversationID, v.Messages, v.ChatCheckpoint, v.ChatSession = picked.ID, picked.Messages, picked.Checkpoint, picked.Session
		return nil
	})
}

// QueueChatCommand queues a command the assistant asked for. It runs once the
// reply under way is finished, ahead of anything else waiting, so it applies
// to the conversation the assistant was in when it asked.
func (s *Service) QueueChatCommand(ctx context.Context, command string) (ChatTurn, error) {
	if command != CommandCompact && command != CommandNew {
		return ChatTurn{}, fmt.Errorf("unknown command %q: %w", command, ErrChatValidation)
	}
	out := ChatTurn{ID: uid(), Message: "/" + command, Command: command, Origin: OriginAssistant, Status: "queued", CreatedAt: s.now().UTC(), Events: []ChatToolEvent{}}
	err := s.store.update(ctx, func(v *Snapshot) error {
		at := len(v.ChatTurns)
		for i, t := range v.ChatTurns {
			if t.Status == "queued" && t.Command == command && t.Origin == OriginAssistant {
				out = t // Asked twice in one reply: once is enough.
				return nil
			}
			if t.Status == "queued" && at == len(v.ChatTurns) {
				at = i
			}
		}
		v.ChatTurns = append(v.ChatTurns[:at], append([]ChatTurn{out}, v.ChatTurns[at:]...)...)
		v.ChatQueueRevision++
		return nil
	})
	return out, err
}

// FinishChatCommand completes a command turn. /new and /clear archive the
// conversation and open a fresh one with an overview of what is going on.
// /compact shows the summary it saved, if it had anything to summarize;
// outcome says what the command did, in a line.
func (s *Service) FinishChatCommand(ctx context.Context, id, summary, outcome string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := chatTurn(v, id)
		if t == nil {
			return ErrNotFound
		}
		if t.Status != "running" || t.Command == "" {
			return ErrConflict
		}
		now := s.now().UTC()
		t.Status, t.FinishedAt, t.Outcome = "completed", &now, outcome
		t.LoadingPhrase, t.ModelStatus, t.RetryAt = "", "", time.Time{}
		switch t.Command {
		case CommandNew, CommandClear:
			archiveConversation(v, now)
			overview := Message{ID: uid(), Role: "assistant", Origin: OriginOverview, Content: conversationOverview(v), CreatedAt: now}
			v.Messages = append(v.Messages, overview)
		case CommandCompact:
			if strings.TrimSpace(summary) != "" {
				t.AssistantMessageID = uid()
				v.Messages = append(v.Messages, Message{ID: t.AssistantMessageID, Role: RoleSummary, Content: summary, CreatedAt: now})
			}
		}
		return nil
	})
}

// archiveConversation puts the current conversation away, turns and all, and
// leaves an empty one in its place. A conversation with nothing said in it
// beyond its overview is not worth keeping.
func archiveConversation(v *Snapshot, now time.Time) {
	id := v.ConversationID
	if id == "" {
		id = uid()
	}
	for i := range v.ChatTurns {
		if t := &v.ChatTurns[i]; t.Status != "queued" && t.Conversation == v.ConversationID {
			t.Conversation = id
		}
	}
	said := false
	for _, m := range v.Messages {
		said = said || m.Origin != OriginOverview
	}
	if said {
		v.Conversations = append(v.Conversations, Conversation{ID: id, Title: conversationTitle(v.Messages), StartedAt: v.Messages[0].CreatedAt, ArchivedAt: now, Messages: v.Messages, Checkpoint: v.ChatCheckpoint, Session: v.ChatSession})
	}
	v.ConversationID, v.Messages, v.ChatCheckpoint, v.ChatSession = uid(), []Message{}, ChatCheckpoint{}, nil
}

// conversationTitle is the owner's first message, on one line.
func conversationTitle(messages []Message) string {
	for _, m := range messages {
		if m.Role == "user" && m.Origin == "" {
			return text.Clip(strings.Join(strings.Fields(m.Content), " "), 80)
		}
	}
	return "Conversation"
}

// conversationOverview is how a fresh conversation opens: the projects, the
// work under way and what is waiting on the owner, from state as it is.
func conversationOverview(v *Snapshot) string {
	var b strings.Builder
	b.WriteString("Fresh start. The previous conversation is saved in History.\n\n")
	titles := map[string]string{}
	for _, p := range v.Projects {
		titles[p.ID] = p.Title
	}
	if len(v.Projects) == 0 {
		b.WriteString("**Projects:** none yet.\n")
	} else {
		fmt.Fprintf(&b, "**Projects (%d)**\n", len(v.Projects))
		for _, p := range v.Projects {
			fmt.Fprintf(&b, "- %s: %s\n", p.Title, p.Status)
		}
	}
	running, queued := []string{}, 0
	for _, t := range v.Tasks {
		switch {
		case t.Finished():
		case t.Status == TaskQueued:
			queued++
		default:
			running = append(running, fmt.Sprintf("- %s (%s): %s", t.Objective, titles[t.ProjectID], t.Status))
		}
	}
	if len(running) == 0 {
		b.WriteString("\n**Running tasks:** none.")
	} else {
		fmt.Fprintf(&b, "\n**Running tasks (%d)**\n%s", len(running), strings.Join(running, "\n"))
	}
	if queued > 0 {
		fmt.Fprintf(&b, "\n%d more waiting to start.", queued)
	}
	b.WriteString("\n")
	open := []string{}
	for _, d := range v.Decisions {
		if d.Status == DecisionOpen {
			line := "- " + d.Title
			if p := titles[d.ProjectID]; p != "" {
				line += " (" + p + ")"
			}
			open = append(open, line)
		}
	}
	if len(open) == 0 {
		b.WriteString("\n**Open decisions:** none.")
	} else {
		fmt.Fprintf(&b, "\n**Open decisions (%d)**\n%s", len(open), strings.Join(open, "\n"))
	}
	return b.String()
}

// What to do with a task implementer's conversation at its next round.
const (
	// WriterCompact has the session compact its own context first. Only Codex
	// can: Claude Code offers no compaction to a session driven from outside.
	WriterCompact = "compact"
	// WriterFresh starts a new session. Nothing is lost that the round needs:
	// its prompt carries the brief, the latest draft and every review.
	WriterFresh = "new"
)

// SetWriterNext asks for a task implementer's conversation to be compacted or
// started afresh. The implementer's session is only ever used at the start of
// a round, so the request waits for the next one; a round already under way
// finishes as it is. Reviewers and QA start fresh every time and a project's
// manager is asked one question at a time, so the implementer is the only
// team member with a conversation to act on.
func (s *Service) SetWriterNext(ctx context.Context, projectID, taskID, next string) (Task, error) {
	if next != WriterCompact && next != WriterFresh {
		return Task{}, fmt.Errorf("the implementer's conversation can be compacted or started afresh: %w", ErrChatValidation)
	}
	return s.UpdateTask(ctx, taskID, func(t *Task, _ *Project) (string, error) {
		if t.ProjectID != projectID {
			return "", ErrNotFound
		}
		if t.Finished() {
			return "", fmt.Errorf("the task is finished; its implementer won't run again: %w", ErrConflict)
		}
		writers := t.RolesOf(RoleImplementer)
		if len(writers) != 1 || len(t.WriterSession) == 0 {
			return "", fmt.Errorf("the implementer has no conversation yet; its first round starts one: %w", ErrConflict)
		}
		if next == WriterCompact && writers[0].Engine != "codex" {
			return "", fmt.Errorf("%s runs on %s, which can't be compacted from outside; start its conversation afresh instead, which loses nothing its next round needs: %w", writers[0].Name, writers[0].Engine, ErrConflict)
		}
		t.WriterNext = next
		t.WriterRequest++
		if next == WriterCompact {
			return fmt.Sprintf("%s will compact its conversation at its next round of %s", writers[0].Name, t.Objective), nil
		}
		return fmt.Sprintf("%s will start afresh at its next round of %s", writers[0].Name, t.Objective), nil
	})
}
