package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

type SummaryQuery struct {
	ChangesOnly   bool   `json:"-"`
	FixedBoundary bool   `json:"-"`
	After         int64  `json:"after"`
	Boundary      int64  `json:"boundary,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
}
type AutopilotSummary struct {
	Acknowledgeable bool                         `json:"acknowledgeable"`
	From            int64                        `json:"from"`
	Boundary        int64                        `json:"boundary"`
	Groups          map[string][]AutopilotAction `json:"groups"`
	Counts          map[string]int               `json:"counts"`
	Decisions       []Decision                   `json:"decisions"`
	Truncated       bool                         `json:"truncated"`
}

// Summary reduces immutable audit copies under a single read transaction. A
// truncated response acknowledges only its scanned boundary, never unseen rows.
func (c *AutopilotCoordinator) Summary(ctx context.Context, q SummaryQuery) (AutopilotSummary, error) {
	out := AutopilotSummary{Acknowledgeable: q.ProjectID == "", From: q.After, Groups: map[string][]AutopilotAction{}, Counts: map[string]int{}, Decisions: []Decision{}}
	if q.After < 0 || q.Boundary < 0 {
		return out, autopilotPageError("invalid summary boundary")
	}
	tx, err := c.service.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var maximum int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM autopilot_audit`).Scan(&maximum); err != nil {
		return out, err
	}
	out.Boundary = maximum
	if q.FixedBoundary || q.Boundary != 0 {
		out.Boundary = q.Boundary
	}
	if out.Boundary > maximum || q.After > out.Boundary {
		return out, autopilotPageError("future summary boundary")
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,payload FROM autopilot_audit WHERE seq>? AND seq<=? ORDER BY seq LIMIT 1001`, q.After, out.Boundary)
	if err != nil {
		return out, err
	}
	latest := map[string]AutopilotAction{}
	scanned := 0
	last := q.After
	for rows.Next() {
		var seq int64
		var data string
		var audit AutopilotAudit
		if err = rows.Scan(&seq, &data); err != nil {
			break
		}
		if scanned == 1000 {
			out.Truncated = true
			out.Boundary = last
			break
		}
		if err = json.Unmarshal([]byte(data), &audit); err != nil {
			break
		}
		scanned++
		last = seq
		if q.ProjectID == "" || audit.Action.Action.ProjectID == q.ProjectID {
			latest[audit.Action.ID] = audit.Action
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	if !q.ChangesOnly {
		// Pending proposals remain visible even if the owner saw their first audit.
		budget := 1000 - scanned
		if budget > 200 {
			budget = 200
		}
		current := out.Boundary == maximum
		if current {
			rows, err = tx.QueryContext(ctx, `SELECT payload FROM autopilot_actions WHERE status='proposed' AND (?='' OR project_id=?) ORDER BY id LIMIT ?`, q.ProjectID, q.ProjectID, budget+1)
		} else {
			rows, err = tx.QueryContext(ctx, `SELECT payload FROM autopilot_audit WHERE seq IN (SELECT MAX(seq) FROM autopilot_audit WHERE seq<=? GROUP BY action_id) AND json_extract(payload,'$.action.status')='proposed' AND (?='' OR project_id=?) ORDER BY seq LIMIT ?`, out.Boundary, q.ProjectID, q.ProjectID, budget+1)
		}
		if err != nil {
			return out, err
		}
		pendingScanned := 0
		for rows.Next() {
			if pendingScanned == budget {
				out.Truncated = true
				break
			}
			pendingScanned++
			var data string
			var audit AutopilotAudit
			if err = rows.Scan(&data); err == nil {
				if current {
					err = json.Unmarshal([]byte(data), &audit.Action)
				} else {
					err = json.Unmarshal([]byte(data), &audit)
				}
			}
			if err != nil {
				break
			}
			if audit.Action.Status == "proposed" && (q.ProjectID == "" || audit.Action.Action.ProjectID == q.ProjectID) {
				latest[audit.Action.ID] = audit.Action
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return out, err
		}
	}

	for _, a := range latest {
		group := a.Status
		if group == "uncertain" || group == "conflict" {
			group = "failed"
		}
		out.Groups[group] = append(out.Groups[group], a)
		if a.Status == "proposed" || a.Status == "conflict" {
			out.Groups["needs_owner"] = append(out.Groups["needs_owner"], a)
		}
	}
	for key := range out.Groups {
		sort.Slice(out.Groups[key], func(i, j int) bool { return out.Groups[key][i].ID < out.Groups[key][j].ID })
		out.Counts[key] = len(out.Groups[key])
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM state WHERE id=1`).Scan(&state); err != nil {
		return out, err
	}
	var disk diskState
	if err = json.Unmarshal([]byte(state), &disk); err != nil {
		return out, err
	}
	if q.ProjectID != "" && project(&disk.Snapshot, q.ProjectID) == nil {
		return out, ErrNotFound
	}
	for _, d := range disk.Snapshot.Decisions {
		if !q.ChangesOnly && d.Status == DecisionOpen && (q.ProjectID == "" || d.ProjectID == q.ProjectID) {
			out.Decisions = append(out.Decisions, d)
		}
	}
	out.Counts["needs_owner"] += len(out.Decisions)
	return out, tx.Commit()
}

// Text is a compact presentation. Full immutable details remain in Groups and
// the paged history; large reasons cannot overflow a chat or notification.
func (s AutopilotSummary) Text() string {
	var b strings.Builder
	b.WriteString("Autopilot summary:")
	excerpt := func(value string) string { return text.Clip(strings.Join(strings.Fields(value), " "), 180) }
	for _, kind := range []string{"proposed", "performed", "failed", "refused", "undone", "cancelled"} {
		actions := s.Groups[kind]
		if len(actions) == 0 {
			continue
		}
		label := kind
		if kind == "failed" {
			label = "failed or unconfirmed"
		}
		fmt.Fprintf(&b, "\n%d %s", len(actions), label)
		for _, a := range actions[:min(len(actions), 3)] {
			fmt.Fprintf(&b, "\n- %s: %s (%s), project %s: %s", a.Status, excerpt(a.Action.Kind), a.Function, excerpt(a.Action.ProjectID), excerpt(a.Reason))
			if a.Action.TaskID != "" {
				fmt.Fprintf(&b, "; task %s", excerpt(a.Action.TaskID))
			}
			if a.Action.Commit != "" {
				fmt.Fprintf(&b, "; commit %s", excerpt(a.Action.Commit))
			}
			if a.Detail != "" {
				fmt.Fprintf(&b, "; %s", excerpt(a.Detail))
			}
		}
		if len(actions) > 3 {
			fmt.Fprintf(&b, "\n- %d more in history", len(actions)-3)
		}
	}
	if len(s.Decisions) > 0 {
		fmt.Fprintf(&b, "\n%d decisions need you", len(s.Decisions))
		for _, d := range s.Decisions[:min(len(s.Decisions), 5)] {
			fmt.Fprintf(&b, "\n- Needs you: %s", excerpt(d.Title))
		}
		if len(s.Decisions) > 5 {
			fmt.Fprintf(&b, "\n- %d more decisions need you", len(s.Decisions)-5)
		}
	}
	if s.Truncated {
		b.WriteString("\nMore activity remains unseen.")
	}
	return b.String()
}

func (c *AutopilotCoordinator) Progress(ctx context.Context, key string) (int64, error) {
	var seq int64
	err := c.service.store.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM autopilot_progress WHERE key=?),0)`, key).Scan(&seq)
	return seq, err
}
func (c *AutopilotCoordinator) AcknowledgeSummary(ctx context.Context, boundary int64) error {
	return c.AdvanceProgress(ctx, "owner_seen", boundary)
}
func (c *AutopilotCoordinator) AdvanceProgress(ctx context.Context, key string, boundary int64) error {
	if boundary < 0 || (key != "owner_seen" && key != "notified") {
		return autopilotPageError("invalid progress")
	}
	return c.service.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		return advanceAutopilotProgress(ctx, conn, key, boundary)
	})
}

func advanceAutopilotProgress(ctx context.Context, conn *sql.Conn, key string, boundary int64) error {
	var maximum int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM autopilot_audit`).Scan(&maximum); err != nil {
		return err
	}
	if boundary < 0 || boundary > maximum {
		return autopilotPageError("future acknowledgement")
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO autopilot_progress(key,seq) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET seq=excluded.seq WHERE excluded.seq>seq`, key, boundary)
	return err
}
func (c *AutopilotCoordinator) UnseenSummary(ctx context.Context) (AutopilotSummary, error) {
	after, err := c.Progress(ctx, "owner_seen")
	if err != nil {
		return AutopilotSummary{}, err
	}
	return c.Summary(ctx, SummaryQuery{After: after})
}

type AutopilotDigest struct {
	LocalDate string    `json:"local_date"`
	From      int64     `json:"from"`
	Boundary  int64     `json:"boundary"`
	CreatedAt time.Time `json:"created_at"`
}

// RecordDigest makes only today, never missed calendar days. The unique local
// date survives restarts and backwards clock jumps; location is caller supplied.
func (c *AutopilotCoordinator) RecordDigest(ctx context.Context, now time.Time, location *time.Location) (*AutopilotDigest, error) {
	settings := c.service.configuration().Autopilot.DailyDigest
	if !settings.Enabled {
		return nil, nil
	}
	at := settings.At
	if at == "" {
		at = "08:00"
	}
	local := now.In(location)
	if local.Format("15:04") < at {
		return nil, nil
	}
	date := local.Format("2006-01-02")
	var exists bool
	if err := c.service.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM autopilot_digests WHERE local_date=?)`, date).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, nil
	}
	var out *AutopilotDigest
	err := c.service.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		d := AutopilotDigest{LocalDate: date, CreatedAt: now.UTC()}
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(to_seq),0) FROM autopilot_digests`).Scan(&d.From); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM autopilot_audit`).Scan(&d.Boundary); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO autopilot_digests(local_date,from_seq,to_seq,created_at) VALUES(?,?,?,?)`, d.LocalDate, d.From, d.Boundary, d.CreatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if n > 0 {
			out = &d
		}
		return err
	})
	return out, err
}
func (c *AutopilotCoordinator) Digests(ctx context.Context, before string, limit int) ([]AutopilotDigest, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, autopilotPageError("invalid digest limit")
	}
	if before != "" {
		if _, err := time.Parse("2006-01-02", before); err != nil {
			return nil, autopilotPageError("invalid local date")
		}
	}
	rows, err := c.service.store.db.QueryContext(ctx, `SELECT local_date,from_seq,to_seq,created_at FROM autopilot_digests WHERE (?='' OR local_date<?) ORDER BY local_date DESC LIMIT ?`, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutopilotDigest{}
	for rows.Next() {
		var d AutopilotDigest
		var at string
		if err := rows.Scan(&d.LocalDate, &d.From, &d.Boundary, &at); err != nil {
			return nil, err
		}
		d.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
