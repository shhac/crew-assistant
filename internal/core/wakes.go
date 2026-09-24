package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A Wake is an agent's request to be woken when something changes, with what
// it wants to do then. The daemon watches; the agent does not poll. Several can
// be waiting at once, each named by its handle so it can be cancelled.
type Wake struct {
	ID string `json:"id"`
	// Owner is who is woken: the assistant, a task's implementer, or the task
	// loop itself (which only looks again and shows nobody the wake).
	Owner     string `json:"owner"`
	TaskID    string `json:"task_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	On        string `json:"on"`
	Target    string `json:"target"`
	// Match fires the wake when the watched value becomes this, rather than on
	// any change from the baseline.
	Match string `json:"match,omitempty"`
	// Prompt is the continuation the agent wrote for itself.
	Prompt string `json:"prompt,omitempty"`
	// Baseline is what the daemon saw when the wake was registered.
	Baseline    string     `json:"baseline"`
	Status      string     `json:"status"`
	Observed    string     `json:"observed,omitempty"`
	Event       string     `json:"event,omitempty"`
	TimedOut    bool       `json:"timed_out,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	FiredAt     *time.Time `json:"fired_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	// Attempts counts wake-up turns that failed to carry this wake.
	Attempts int `json:"attempts,omitempty"`
}

const (
	WakeAssistant = "assistant"
	WakeTask      = "task"
	WakeLoop      = "loop"

	WakeWaiting   = "waiting"
	WakeFired     = "fired"
	WakeDelivered = "delivered"
	WakeCancelled = "cancelled"

	WakeOnTask     = "task"
	WakeOnBranch   = "branch"
	WakeOnChecks   = "pr_checks"
	WakeOnReview   = "pr_review"
	WakeOnTime     = "time"
	DefaultWakeFor = 24 * time.Hour
	MaxWakeFor     = 7 * 24 * time.Hour
	MaxWaiting     = 20
	keepFinished   = 200
)

type WakeInput struct {
	Owner, TaskID, ProjectID string
	On, Target, Match        string
	Prompt                   string
	Baseline                 string
	Timeout                  time.Duration
}

var wakeKinds = map[string]bool{WakeOnTask: true, WakeOnBranch: true, WakeOnChecks: true, WakeOnReview: true, WakeOnTime: true}

// RegisterWake records a new wake with the baseline the caller observed.
func (s *Service) RegisterWake(ctx context.Context, in WakeInput) (Wake, error) {
	if !wakeKinds[in.On] {
		return Wake{}, fmt.Errorf("cannot wait on %q; use task, branch, pr_checks, pr_review or time", in.On)
	}
	if in.Owner != WakeAssistant && in.Owner != WakeTask && in.Owner != WakeLoop {
		return Wake{}, errors.New("unknown wake owner")
	}
	if (in.Owner == WakeTask || in.Owner == WakeLoop) && in.TaskID == "" {
		return Wake{}, errors.New("a task's wake names its task")
	}
	if strings.TrimSpace(in.Target) == "" || len(in.Target) > 300 || len(in.Match) > 100 || len(in.Prompt) > 4000 {
		return Wake{}, errors.New("a wake needs a target; target, match and prompt have length limits")
	}
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = DefaultWakeFor
	}
	if timeout > MaxWakeFor {
		return Wake{}, fmt.Errorf("a wake can wait at most %s", MaxWakeFor)
	}
	now := s.now().UTC()
	out := Wake{ID: "wake-" + uid()[:8], Owner: in.Owner, TaskID: in.TaskID, ProjectID: in.ProjectID, On: in.On, Target: strings.TrimSpace(in.Target), Match: strings.TrimSpace(in.Match), Prompt: strings.TrimSpace(in.Prompt), Baseline: in.Baseline, Status: WakeWaiting, CreatedAt: now, ExpiresAt: now.Add(timeout)}
	err := s.store.update(ctx, func(v *Snapshot) error {
		waiting := 0
		for _, w := range v.Wakes {
			if w.Status == WakeWaiting && w.Owner == in.Owner && w.TaskID == in.TaskID {
				waiting++
			}
		}
		if waiting >= MaxWaiting {
			return fmt.Errorf("already waiting on %d things; cancel some first: %w", waiting, ErrConflict)
		}
		if in.TaskID != "" && task(v, in.TaskID) == nil {
			return ErrNotFound
		}
		v.Wakes = append(v.Wakes, out)
		return nil
	})
	return out, err
}

// CancelWake stops a wake that has not been delivered. A task's implementer
// may only cancel its own task's wakes; the assistant, like the owner, may
// cancel any.
func (s *Service) CancelWake(ctx context.Context, id, taskID string) (Wake, error) {
	var out Wake
	err := s.store.update(ctx, func(v *Snapshot) error {
		for i := range v.Wakes {
			w := &v.Wakes[i]
			if w.ID != id {
				continue
			}
			if taskID != "" && w.TaskID != taskID {
				return ErrNotFound
			}
			if w.Status != WakeWaiting && w.Status != WakeFired {
				return fmt.Errorf("%s is already %s: %w", id, w.Status, ErrConflict)
			}
			w.Status = WakeCancelled
			removeFromWakeTurns(v, id, s.now().UTC())
			out = *w
			return nil
		}
		return ErrNotFound
	})
	return out, err
}

// Waiting returns every wake still being watched.
func (s *Service) Waiting(ctx context.Context) ([]Wake, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	var out []Wake
	for _, w := range snap.Wakes {
		if w.Status == WakeWaiting {
			out = append(out, w)
		}
	}
	return out, nil
}

// FireWake records the change the watcher saw. An assistant's wake joins the
// queued wake-up turn, or starts one; a task's waits for its next round.
func (s *Service) FireWake(ctx context.Context, id, observed, event string, timedOut bool) (Wake, error) {
	var out Wake
	err := s.store.update(ctx, func(v *Snapshot) error {
		for i := range v.Wakes {
			w := &v.Wakes[i]
			if w.ID != id {
				continue
			}
			if w.Status != WakeWaiting {
				return fmt.Errorf("%s is %s: %w", id, w.Status, ErrConflict)
			}
			now := s.now().UTC()
			w.Status, w.Observed, w.Event, w.TimedOut, w.FiredAt = WakeFired, observed, event, timedOut, &now
			if w.Owner == WakeAssistant {
				queueWakeTurn(v, w.ID, now)
			}
			pruneWakes(v)
			out = *w
			return nil
		}
		return ErrNotFound
	})
	return out, err
}

// TakeTaskWakes hands a task's fired wakes to the round about to start and
// marks them delivered.
func (s *Service) TakeTaskWakes(ctx context.Context, taskID, owner string) ([]Wake, error) {
	var out []Wake
	err := s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		for i := range v.Wakes {
			w := &v.Wakes[i]
			if w.TaskID == taskID && w.Owner == owner && w.Status == WakeFired {
				w.Status, w.DeliveredAt = WakeDelivered, &now
				out = append(out, *w)
			}
		}
		return nil
	})
	return out, err
}

// FiresOn reports whether an observed value wakes w: it has become Match, or,
// with no Match, it differs from what was seen at registration. A Match also
// names a value it heads before an "@", such as SUCCESS for a pull request's
// "SUCCESS@abc1234/CLEAN/OPEN".
func (w Wake) FiresOn(value string) bool {
	if w.Match == "" {
		return value != w.Baseline
	}
	return value == w.Match || strings.HasPrefix(value, w.Match+"@")
}

// settleTaskWakes fires every waiting wake on a task whose status now
// matches, or has changed from what was seen when the wake was registered.
func settleTaskWakes(v *Snapshot, now time.Time) {
	for i := range v.Wakes {
		w := &v.Wakes[i]
		if w.Status != WakeWaiting || w.On != WakeOnTask {
			continue
		}
		t := task(v, w.Target)
		if t == nil {
			continue
		}
		if !w.FiresOn(t.Status) {
			continue
		}
		w.Status, w.Observed, w.FiredAt = WakeFired, t.Status, &now
		w.Event = fmt.Sprintf("%q is now %s", t.Objective, t.Status)
		if t.Detail != "" {
			w.Event += ": " + t.Detail
		}
		if w.Owner == WakeAssistant {
			queueWakeTurn(v, w.ID, now)
		}
	}
}

// queueWakeTurn adds the wake to the assistant's queued wake-up turn, so
// several that fire while it is busy arrive together, each with its own times.
func queueWakeTurn(v *Snapshot, id string, now time.Time) {
	for i := range v.ChatTurns {
		t := &v.ChatTurns[i]
		if t.Status == "queued" && t.Origin == OriginWake {
			t.WakeIDs = append(t.WakeIDs, id)
			return
		}
	}
	v.ChatTurns = append(v.ChatTurns, ChatTurn{ID: uid(), Message: "Wake-up", Status: "queued", CreatedAt: now, Origin: OriginWake, WakeIDs: []string{id}})
	v.ChatQueueRevision++
}

func removeFromWakeTurns(v *Snapshot, id string, now time.Time) {
	for i := range v.ChatTurns {
		t := &v.ChatTurns[i]
		if t.Status != "queued" || t.Origin != OriginWake {
			continue
		}
		kept := t.WakeIDs[:0]
		for _, w := range t.WakeIDs {
			if w != id {
				kept = append(kept, w)
			}
		}
		t.WakeIDs = kept
		if len(kept) == 0 {
			t.Status, t.FinishedAt = "cancelled", &now
			v.ChatQueueRevision++
		}
	}
}

// cancelTaskWakes ends a finished task's wakes: nobody is left to wake.
func cancelTaskWakes(v *Snapshot, taskID string) {
	for i := range v.Wakes {
		w := &v.Wakes[i]
		if w.TaskID == taskID && (w.Status == WakeWaiting || w.Status == WakeFired) {
			w.Status = WakeCancelled
		}
	}
}

func pruneWakes(v *Snapshot) {
	finished := 0
	for _, w := range v.Wakes {
		if w.Status == WakeDelivered || w.Status == WakeCancelled {
			finished++
		}
	}
	if finished <= keepFinished {
		return
	}
	drop := finished - keepFinished
	kept := v.Wakes[:0]
	for _, w := range v.Wakes {
		if drop > 0 && (w.Status == WakeDelivered || w.Status == WakeCancelled) {
			drop--
			continue
		}
		kept = append(kept, w)
	}
	v.Wakes = kept
}

// WakeReport is what an agent reads when woken: the handle, the times, what
// changed and its own continuation, ending with a reminder to check the
// current state because it may be reading something stale.
func WakeReport(wakes []Wake, now time.Time) string {
	var b strings.Builder
	for _, w := range wakes {
		fmt.Fprintf(&b, "%s — waiting on %s %s", w.ID, w.On, w.Target)
		if w.Match != "" {
			fmt.Fprintf(&b, " to become %q", w.Match)
		}
		b.WriteString("\n")
		seen := w.CreatedAt
		if w.FiredAt != nil {
			seen = *w.FiredAt
		}
		fmt.Fprintf(&b, "  registered %s · seen %s · delivered %s (%s after it was seen)\n", stamp(w.CreatedAt), stamp(seen), stamp(now), now.Sub(seen).Round(time.Second))
		if w.TimedOut {
			fmt.Fprintf(&b, "  timed out: nothing changed before %s; it was still %q\n", stamp(w.ExpiresAt), w.Baseline)
		} else {
			fmt.Fprintf(&b, "  before: %q · after: %q\n", w.Baseline, w.Observed)
		}
		if w.Event != "" {
			fmt.Fprintf(&b, "  what happened: %s\n", w.Event)
		}
		if w.Prompt != "" {
			fmt.Fprintf(&b, "  your continuation: %s\n", w.Prompt)
		}
	}
	b.WriteString("Things may have changed since each was seen; check the current state before acting on it.")
	return b.String()
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
