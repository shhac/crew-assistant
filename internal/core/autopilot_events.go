package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const autopilotEventTime = "2006-01-02T15:04:05.000000000Z"

// AutopilotEvent is durable input, not authority to act.
type AutopilotEvent struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	ProjectID  string          `json:"project_id,omitempty"`
	TaskID     string          `json:"task_id,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

func insertAutopilotEvent(ctx context.Context, conn *sql.Conn, e AutopilotEvent) error {
	_, err := insertAutopilotEventRows(ctx, conn, e)
	return err
}

func insertAutopilotEventRows(ctx context.Context, conn *sql.Conn, e AutopilotEvent) (int64, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	result, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO autopilot_events(id,kind,project_id,task_id,occurred_at,status,payload) VALUES(?,?,?,?,?,'pending',?)`, e.ID, e.Kind, e.ProjectID, e.TaskID, e.OccurredAt.UTC().Format(autopilotEventTime), string(data))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Only capture fields that event identities depend on, before mutation. Avoid
// cloning the full project record on every transaction.
type autopilotCaptureState struct {
	decisions map[string]string
	landed    map[string]bool
	releases  map[string]bool
}

func captureAutopilotState(v Snapshot) autopilotCaptureState {
	before := autopilotCaptureState{map[string]string{}, map[string]bool{}, map[string]bool{}}
	for _, d := range v.Decisions {
		before.decisions[d.ID] = d.Status
	}
	for _, t := range v.Tasks {
		if t.Status == TaskLanded {
			before.landed[t.ID] = true
		}
	}
	for _, p := range v.Projects {
		for _, r := range p.Releases {
			before.releases[p.ID+":"+r.Version] = true
		}
	}
	return before
}

func captureAutopilotEvents(ctx context.Context, conn *sql.Conn, before autopilotCaptureState, after Snapshot, now time.Time, inserted *bool) error {
	put := func(id, kind, projectID, taskID string) error {
		n, err := insertAutopilotEventRows(ctx, conn, AutopilotEvent{ID: id, Kind: kind, ProjectID: projectID, TaskID: taskID, OccurredAt: now})
		*inserted = *inserted || n > 0
		return err
	}
	for _, d := range after.Decisions {
		if d.Status == before.decisions[d.ID] || (d.Status != DecisionOpen && d.Status != DecisionResolved) {
			continue
		}
		kind := "decision.opened"
		if d.Status == DecisionResolved {
			kind = "decision.resolved"
		}
		if err := put(kind+":"+d.ID, kind, d.ProjectID, d.TaskID); err != nil {
			return err
		}
	}
	for _, t := range after.Tasks {
		if t.Status == TaskLanded && !before.landed[t.ID] {
			n := 0
			if len(t.Revisions) > 0 {
				n = t.Revisions[len(t.Revisions)-1].N
			}
			if err := put(fmt.Sprintf("task.landed:%s:%d", t.ID, n), "task.landed", t.ProjectID, t.ID); err != nil {
				return err
			}
		}
	}
	for _, p := range after.Projects {
		for _, r := range p.Releases {
			key := p.ID + ":" + r.Version
			if !before.releases[key] {
				if err := put("release.completed:"+key, "release.completed", p.ID, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type CIEvent struct {
	Provider  string `json:"provider"`
	Repo      string `json:"repo"`
	Ref       string `json:"ref"`
	Commit    string `json:"commit"`
	Check     string `json:"check"`
	State     string `json:"state"`
	URL       string `json:"url,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// RecordCIEvent is an ingestion seam; it does not monitor CI.
func (s *Service) RecordCIEvent(ctx context.Context, e CIEvent) error {
	for _, value := range []string{e.Provider, e.Repo, e.Ref, e.Commit, e.Check, e.State} {
		if strings.TrimSpace(value) == "" || len(value) > 512 {
			return errors.New("CI identity fields are required and bounded")
		}
	}
	if len(e.URL) > 2048 || len(e.ProjectID) > 128 {
		return errors.New("CI metadata exceeds bounds")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	// Check participates in identity so independent checks cannot mask one another.
	identity, _ := json.Marshal([]string{e.Provider, e.Repo, e.Ref, e.Commit, e.Check, e.State})
	id := fmt.Sprintf("ci:%x", sha256.Sum256(identity))
	return s.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		return insertAutopilotEvent(ctx, conn, AutopilotEvent{ID: id, Kind: "ci.result", ProjectID: e.ProjectID, OccurredAt: s.now().UTC(), Payload: data})
	})
}

func (s *Service) RecordAutopilotHeartbeat(ctx context.Context, now time.Time) error {
	now = now.UTC().Truncate(30 * time.Minute)
	return s.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		var newer bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM autopilot_events WHERE kind='heartbeat' AND occurred_at>=?)`, now.Format(autopilotEventTime)).Scan(&newer); err != nil {
			return err
		}
		if newer {
			return nil
		}
		if _, err := conn.ExecContext(ctx, `UPDATE autopilot_events SET status='expired',payload='{}' WHERE kind='heartbeat' AND status='pending' AND occurred_at<?`, now.Format(autopilotEventTime)); err != nil {
			return err
		}
		if err := insertAutopilotEvent(ctx, conn, AutopilotEvent{ID: "heartbeat:" + now.Format(time.RFC3339), Kind: "heartbeat", OccurredAt: now}); err != nil {
			return err
		}
		// Completed event identities remain as replay tombstones; old delivery
		// payloads are unnecessary once that identity is terminal.
		if _, err := conn.ExecContext(ctx, `DELETE FROM autopilot_event_deliveries WHERE EXISTS (SELECT 1 FROM autopilot_events e WHERE e.id=autopilot_event_deliveries.event_id AND e.status IN ('delivered','expired') AND e.occurred_at<?)`, now.Add(-7*24*time.Hour).Format(autopilotEventTime)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM autopilot_event_deliveries WHERE event_id IN (SELECT id FROM autopilot_events WHERE kind='heartbeat' AND occurred_at<?)`, now.Add(-7*24*time.Hour).Format(autopilotEventTime)); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `DELETE FROM autopilot_events WHERE kind='heartbeat' AND occurred_at<?`, now.Add(-7*24*time.Hour).Format(autopilotEventTime))
		return err
	})
}

type EventProposal struct {
	Key    string
	Reason string
	Action ConcreteAction
}
type eventSubscription struct {
	kinds   map[string]bool
	handler func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error)
}

// OnEvents is available only to a trusted registered Go capability.
func (f *AutopilotFunction) OnEvents(kinds []string, handler func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error)) error {
	c := f.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.functions[f.id] != f || handler == nil || len(kinds) == 0 {
		return errors.New("event subscription requires registered authority and handler")
	}
	set := map[string]bool{}
	for _, kind := range kinds {
		if kind == "" || set[kind] {
			return errors.New("invalid event kinds")
		}
		set[kind] = true
	}
	f.events = &eventSubscription{set, handler}
	return nil
}

// ConsumeEvents processes one bounded restart batch. The caller serializes passes
// and supplies graceful admission separately from the in-flight force context.
func (c *AutopilotCoordinator) ConsumeEvents(ctx context.Context, now time.Time, stopping func() bool) error {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	completed := false
	defer func() {
		if !completed {
			c.eventAfterTime, c.eventAfterID = "", ""
		}
	}()
	c.mu.Lock()
	subscriptions := map[*AutopilotFunction]*eventSubscription{}
	for _, f := range c.functions {
		if f.events != nil {
			subscriptions[f] = f.events
		}
	}
	c.mu.Unlock()
	rows, err := c.service.store.db.QueryContext(ctx, `SELECT payload FROM autopilot_events WHERE status='pending' AND (occurred_at>? OR (occurred_at=? AND id>?)) ORDER BY occurred_at,id LIMIT 200`, c.eventAfterTime, c.eventAfterTime, c.eventAfterID)
	if err != nil {
		return err
	}
	events := []AutopilotEvent{}
	for rows.Next() {
		var data string
		var e AutopilotEvent
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal([]byte(data), &e)
		}
		if err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		c.eventAfterTime, c.eventAfterID = "", ""
		return nil
	}
	if stopping() || ctx.Err() != nil {
		return ctx.Err()
	}
	expired := []AutopilotEvent{}
	active := []AutopilotEvent{}
	for _, e := range events {
		if now.Sub(e.OccurredAt) > 7*24*time.Hour {
			expired = append(expired, e)
		} else {
			active = append(active, e)
		}
	}
	if len(expired) > 0 {
		err := c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
			count := int64(0)
			for _, e := range expired {
				result, err := conn.ExecContext(ctx, `UPDATE autopilot_events SET status='expired',payload='{}' WHERE id=? AND status='pending'`, e.ID)
				if err != nil {
					return err
				}
				n, err := result.RowsAffected()
				if err != nil {
					return err
				}
				count += n
			}
			if count > 0 {
				record(v, now.UTC(), "", "autopilot.event.expired", fmt.Sprintf("Expired %d autopilot events older than seven days", count))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	var snapshot Snapshot
	version := int64(-1)
	if err := c.refreshEventSnapshot(ctx, &snapshot, &version); err != nil {
		return err
	}
	for _, e := range active {
		if stopping() || ctx.Err() != nil {
			return ctx.Err()
		}

		c.eventAfterTime, c.eventAfterID = e.OccurredAt.UTC().Format(autopilotEventTime), e.ID
		if err := c.refreshEventSnapshot(ctx, &snapshot, &version); err != nil {
			return err
		}
		if err := c.checkEventAdmission(snapshot, e.ProjectID); err != nil {
			if errors.Is(err, ErrAutopilotDeferred) {
				continue
			}
			return err
		}
		deferred := false
		for f, subscription := range subscriptions {
			if !subscription.kinds[e.Kind] {
				continue
			}
			var status, retry, detail string
			attempts := 0
			err = c.service.store.db.QueryRowContext(ctx, `SELECT status,attempts,retry_at,detail FROM autopilot_event_deliveries WHERE event_id=? AND function_id=?`, e.ID, f.id).Scan(&status, &attempts, &retry, &detail)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if status == "delivered" || status == "failed" || status == "conflict" {
				continue
			}
			if retry != "" {
				at, err := time.Parse(time.RFC3339Nano, retry)
				if err != nil {
					return err
				}
				if now.Before(at) {
					deferred = true
					continue
				}
			}
			outcomeDetail := ""
			if err := c.refreshEventSnapshot(ctx, &snapshot, &version); err != nil {
				return err
			}
			if err := c.checkEventAdmission(snapshot, e.ProjectID); err != nil {
				if errors.Is(err, ErrAutopilotDeferred) {
					deferred = true
					continue
				}
				return err
			}
			proposals, deliveryErr := subscription.handler(ctx, snapshot, e)
			if deliveryErr == nil {
				outcomeDetail, deliveryErr = f.submitEventProposals(ctx, e, proposals)
			}
			if errors.Is(deliveryErr, ErrAutopilotDeferred) || ctx.Err() != nil {
				deferred = true
				continue
			}
			status = "delivered"
			detail = outcomeDetail
			retry = ""
			if deliveryErr != nil {
				detail = deliveryErr.Error()
				attempts++
				status = "pending"
				deferred = true
				retry = now.Add(time.Duration(1<<attempts) * time.Minute).UTC().Format(time.RFC3339Nano)
				if errors.Is(deliveryErr, ErrConflict) {
					status = "conflict"
				} else if attempts >= 5 {
					status = "failed"
				}
			}
			writeDelivery := func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `INSERT INTO autopilot_event_deliveries(event_id,function_id,status,attempts,retry_at,detail) VALUES(?,?,?,?,?,?) ON CONFLICT(event_id,function_id) DO UPDATE SET status=excluded.status,attempts=excluded.attempts,retry_at=excluded.retry_at,detail=excluded.detail`, e.ID, f.id, status, attempts, retry, detail)
				return err
			}
			if status == "failed" || status == "conflict" {
				err = c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
					if err := writeDelivery(conn); err != nil {
						return err
					}
					record(v, now, e.ProjectID, "autopilot.event."+status, fmt.Sprintf("%s: %s: %s", e.ID, f.id, detail))
					return nil
				})
			} else {
				err = c.service.store.updateAutopilotEvents(ctx, writeDelivery)
			}
			if err != nil {
				return err
			}
		}
		if !deferred {
			if err := c.finishEvent(ctx, e); err != nil {
				return err
			}
		}
	}
	completed = true
	if len(events) < 200 {
		c.eventAfterTime, c.eventAfterID = "", ""
	} else {
		last := events[len(events)-1]
		c.eventAfterTime, c.eventAfterID = last.OccurredAt.UTC().Format(autopilotEventTime), last.ID
	}
	return nil
}

// Check the cheap durable revision before each handler. Unchanged state is
// decoded once per pass; committed effects and owner edits refresh handler input.
// Reading the revision on both sides of Snapshot prevents caching a snapshot
// under a revision written concurrently with that read.
func (c *AutopilotCoordinator) refreshEventSnapshot(ctx context.Context, snapshot *Snapshot, version *int64) error {
	readVersion := func() (int64, error) {
		var current int64
		err := c.service.store.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM state WHERE id=1),0)`).Scan(&current)
		return current, err
	}
	for {
		current, err := readVersion()
		if err != nil {
			return err
		}
		if current == *version {
			return nil
		}
		fresh, err := c.service.Snapshot(ctx)
		if err != nil {
			return err
		}
		after, err := readVersion()
		if err != nil {
			return err
		}
		if after == current {
			*snapshot, *version = fresh, current
			return nil
		}
	}
}

func (c *AutopilotCoordinator) finishEvent(ctx context.Context, e AutopilotEvent) error {
	return c.service.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE autopilot_events SET status='delivered',payload='{}' WHERE id=? AND status='pending'`, e.ID)
		return err
	})
}

func (s *Service) AutopilotNudges() <-chan struct{} { return s.store.autopilotNudge }

func (c *AutopilotCoordinator) checkEventAdmission(snapshot Snapshot, projectID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.service.store.mu.Lock()
	defer c.service.store.mu.Unlock()
	return c.eventAdmission(snapshot, projectID)
}

// Event-only writes do not serialize or rewrite the project state. SQLite's
// connection-local change counter is constant-time and detects duplicate no-ops.
func (s *Store) updateAutopilotEvents(ctx context.Context, fn func(*sql.Conn) error) error {
	s.mu.Lock()
	committed := false
	defer func() {
		s.mu.Unlock()
		if committed {
			select {
			case s.autopilotNudge <- struct{}{}:
			default:
			}
		}
	}()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var before, after int64
	if err = conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return err
	}
	if err = fn(conn); err != nil {
		return err
	}
	if err = conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	committed = err == nil && after > before
	return err
}

// Validate the handler's envelope before any effects. Each valid key is
// independent: a replay conflict or failed submission cannot discard later keys.
func (f *AutopilotFunction) submitEventProposals(ctx context.Context, e AutopilotEvent, proposals []EventProposal) (string, error) {
	if len(proposals) > 200 {
		return "", errors.New("too many event proposals")
	}
	keys := map[string]bool{}
	for _, p := range proposals {
		if p.Key == "" || len(p.Key) > 64 || keys[p.Key] {
			return "", errors.New("proposal keys must be unique and bounded")
		}
		keys[p.Key] = true
	}
	detail := ""
	var conflicts []string
	var failures []error
	for _, p := range proposals {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		source := fmt.Sprintf("event:%x", sha256.Sum256([]byte(e.ID+"\x00"+f.id+"\x00"+p.Key)))
		receipt, err := f.SubmitEvent(ctx, source, p.Reason, p.Action)
		if errors.Is(err, ErrConflict) {
			conflicts = append(conflicts, fmt.Sprintf("proposal key %q: conflict", p.Key))
		} else if err != nil {
			failures = append(failures, fmt.Errorf("proposal key %q: %w", p.Key, err))
		} else if receipt.ID == "" {
			detail = "Function Off: no action recorded"
		}
	}
	// Retry transient failures even when another key conflicted. The conflicting
	// key remains fenced by its original source on every replay.
	if len(failures) > 0 {
		if len(conflicts) > 0 {
			failures = append(failures, errors.New(strings.Join(conflicts, "; ")))
		}
		return detail, errors.Join(failures...)
	}
	if len(conflicts) > 0 {
		return detail, fmt.Errorf("%w: %s", ErrConflict, strings.Join(conflicts, "; "))
	}
	return detail, nil
}
