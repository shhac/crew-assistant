package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The owner can revise a queued message and change the order messages will run
// in. The guarantee those operations protect is narrow: the queue the owner is
// looking at is the queue that runs. Nothing here assumes queued messages
// depend on each other — that is the owner's judgment — only that an order
// they never saw must not execute.
//
// A hold names a queued turn and blocks starting it and everything after it.
// Turns ahead of it keep running. Its expiry is what stops a closed browser
// from wedging the queue: the client refreshes the lease while an editor or a
// drag is open, and the queue resumes on its own once that stops.
//
// The daemon never holds unsaved text. An edit is a save, and a turn's message
// only enters the conversation when the turn starts, so a lapsed lease simply
// resumes the queue with the last saved text.
type ChatHold struct {
	TurnID    string    `json:"turn_id"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MaxChatHold bounds a lease so a client that stops refreshing cannot hold the
// queue for longer than an owner would plausibly be mid-edit.
const MaxChatHold = 2 * time.Minute

var ErrChatHeld = errors.New("the queue is held while a message is being changed")

func validHoldReason(reason string) bool {
	return reason == "editing" || reason == "reordering"
}

// Active reports whether a hold still blocks the queue at the given time.
func (h *ChatHold) Active(now time.Time) bool {
	return h != nil && h.TurnID != "" && now.Before(h.ExpiresAt)
}

// liveHold reports the hold that is really in force. A hold naming a turn that
// has since started or been cancelled is spent: leaving it standing would keep
// telling the owner the queue is paused and would refuse every other change
// until it lapsed.
func liveHold(v *Snapshot, now time.Time) *ChatHold {
	if !v.ChatHold.Active(now) {
		return nil
	}
	for i := range v.ChatTurns {
		if v.ChatTurns[i].ID == v.ChatHold.TurnID {
			if v.ChatTurns[i].Status != "queued" {
				return nil
			}
			return v.ChatHold
		}
	}
	return nil
}

func queuedTurn(v *Snapshot, id string) (*ChatTurn, error) {
	for i := range v.ChatTurns {
		if v.ChatTurns[i].ID != id {
			continue
		}
		if v.ChatTurns[i].Status != "queued" {
			return nil, fmt.Errorf("this message already started: %w", ErrConflict)
		}
		return &v.ChatTurns[i], nil
	}
	return nil, ErrNotFound
}

// HoldChat takes or refreshes the lease on a queued turn. Taking a hold on a
// different turn while one is live is refused rather than silently stealing it,
// so two editors cannot each believe they have the queue.
func (s *Service) HoldChat(ctx context.Context, id, reason string, ttl time.Duration) (ChatHold, error) {
	if !validHoldReason(reason) {
		return ChatHold{}, fmt.Errorf("hold reason must be editing or reordering: %w", ErrChatValidation)
	}
	if ttl <= 0 || ttl > MaxChatHold {
		ttl = MaxChatHold
	}
	var out ChatHold
	err := s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		if _, err := queuedTurn(v, id); err != nil {
			return err
		}
		if live := liveHold(v, now); live != nil && live.TurnID != id {
			return fmt.Errorf("another message is being changed: %w", ErrConflict)
		}
		out = ChatHold{TurnID: id, Reason: reason, ExpiresAt: now.Add(ttl)}
		v.ChatHold = &out
		return nil
	})
	return out, err
}

// ReleaseChatHold ends a lease early. Releasing a lease that already lapsed or
// belongs to another turn is not an error: the caller's intent is satisfied.
func (s *Service) ReleaseChatHold(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if v.ChatHold != nil && v.ChatHold.TurnID == id {
			v.ChatHold = nil
		}
		return nil
	})
}

// EditChatMessage replaces the text of a queued turn. The revision the editor
// was opened against is required, so an edit that lost a race with the daemon
// starting the turn is refused rather than applied to a running turn. The turn
// keeps its identity, so the key that protects against duplicate delivery still
// holds; its revision moves, so text the daemon already read cannot start.
func (s *Service) EditChatMessage(ctx context.Context, id, message string, revision int) (ChatTurn, error) {
	if strings.TrimSpace(message) == "" || len(message) > 24000 {
		return ChatTurn{}, fmt.Errorf("message must contain 1–24000 characters: %w", ErrChatValidation)
	}
	var out ChatTurn
	err := s.store.update(ctx, func(v *Snapshot) error {
		turn, err := queuedTurn(v, id)
		if err != nil {
			return err
		}
		if turn.Origin == OriginWake {
			return fmt.Errorf("a wake-up is written by the daemon, not edited; cancel it instead: %w", ErrConflict)
		}
		if turn.Revision != revision {
			return fmt.Errorf("this message changed since you opened it: %w", ErrConflict)
		}
		turn.Message = strings.TrimSpace(message)
		turn.Revision++
		out = *turn
		return nil
	})
	return out, err
}

// reorderQueued rebuilds the turns with their queued entries in the requested
// order. Turns that have started keep their place. A set of ids that does not
// name exactly the queued turns is a malformed order, not a stale one.
func reorderQueued(turns []ChatTurn, ids []string) ([]ChatTurn, error) {
	queued := map[string]*ChatTurn{}
	count := 0
	for i := range turns {
		if turns[i].Status == "queued" {
			queued[turns[i].ID] = &turns[i]
			count++
		}
	}
	if len(ids) != count {
		return nil, fmt.Errorf("the order must name every queued message exactly once: %w", ErrChatValidation)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if queued[id] == nil || seen[id] {
			return nil, fmt.Errorf("the order must name every queued message exactly once: %w", ErrChatValidation)
		}
		seen[id] = true
	}
	next, slot := make([]ChatTurn, 0, len(turns)), 0
	for i := range turns {
		if turns[i].Status != "queued" {
			next = append(next, turns[i])
			continue
		}
		next = append(next, *queued[ids[slot]])
		slot++
	}
	return next, nil
}

// ReorderChat sets the order queued turns will run in. The caller names the
// whole intended order and the queue revision it was decided against, so a
// queue that changed underneath is refused rather than merged.
func (s *Service) ReorderChat(ctx context.Context, ids []string, revision int) ([]ChatTurn, error) {
	var out []ChatTurn
	err := s.store.update(ctx, func(v *Snapshot) error {
		if v.ChatQueueRevision != revision {
			return fmt.Errorf("the queue changed since you saw it: %w", ErrConflict)
		}
		next, err := reorderQueued(v.ChatTurns, ids)
		if err != nil {
			return err
		}
		v.ChatTurns = next
		v.ChatQueueRevision++
		out = append([]ChatTurn{}, v.ChatTurns...)
		return nil
	})
	return out, err
}

// ChatQueueState reports a live hold and the queue revision. A lapsed hold is
// reported as absent so the dashboard never shows a block that is not real.
func (s *Service) ChatQueueState(ctx context.Context) (*ChatHold, int, error) {
	v, err := s.store.Snapshot(ctx)
	live := liveHold(&v, s.now().UTC())
	if live == nil {
		return nil, v.ChatQueueRevision, err
	}
	hold := *live
	return &hold, v.ChatQueueRevision, err
}
