package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrAutopilotPage = errors.New("invalid autopilot pagination")

func autopilotPageError(detail string) error {
	return fmt.Errorf("%w: %s", ErrAutopilotPage, detail)
}

type AutopilotHistoryQuery struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Cursor    string `json:"cursor"`
	Limit     int    `json:"limit"`
	Forward   bool   `json:"forward"`
	After     int64  `json:"after"`
}
type AutopilotHistory struct {
	Entries  []AutopilotAudit `json:"entries"`
	Next     string           `json:"next,omitempty"`
	Boundary int64            `json:"boundary"`
}
type autopilotCursor struct {
	Project  string `json:"project"`
	Task     string `json:"task"`
	Forward  bool   `json:"forward"`
	Boundary int64  `json:"boundary"`
	Position int64  `json:"position"`
}

func (c *AutopilotCoordinator) Action(ctx context.Context, id string) (AutopilotAction, error) {
	conn, err := c.service.store.db.Conn(ctx)
	if err != nil {
		return AutopilotAction{}, err
	}
	defer conn.Close()
	return readAutopilotAction(ctx, conn, "id", id)
}

// Pending is bounded and paged by immutable IDs; audit transitions have their
// own sequence cursor. History never leaks into the unbounded state response.
func (c *AutopilotCoordinator) Pending(ctx context.Context, after string, limit int) ([]AutopilotAction, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 || len(after) > 64 {
		return nil, autopilotPageError("invalid pending pagination")
	}
	rows, err := c.service.store.db.QueryContext(ctx, "SELECT payload FROM autopilot_actions WHERE status='proposed' AND id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutopilotAction{}
	for rows.Next() {
		var data string
		var a AutopilotAction
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// History pages immutable transition copies under a fixed high-water mark.
// Forward reads are the bounded summary/event-consumer seam for CA-103.
func (c *AutopilotCoordinator) History(ctx context.Context, q AutopilotHistoryQuery) (AutopilotHistory, error) {
	var out AutopilotHistory
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 200 || q.After < 0 || (!q.Forward && q.After != 0) || len(q.Cursor) > 2048 {
		return out, autopilotPageError("invalid history pagination")
	}
	conn, err := c.service.store.db.Conn(ctx)
	if err != nil {
		return out, err
	}
	defer conn.Close()
	v, err := readState(ctx, conn)
	if err != nil {
		return out, err
	}
	if q.ProjectID != "" && project(&v, q.ProjectID) == nil {
		return out, ErrNotFound
	}
	if q.TaskID != "" {
		t := task(&v, q.TaskID)
		if t == nil || q.ProjectID == "" || t.ProjectID != q.ProjectID || t.ID != q.TaskID {
			return out, autopilotPageError("task filter requires its project")
		}
	}
	cursor := autopilotCursor{Project: q.ProjectID, Task: q.TaskID, Forward: q.Forward, Position: q.After}
	if q.Cursor != "" {
		if q.After != 0 {
			return out, autopilotPageError("cursor and after cannot be combined")
		}
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return out, autopilotPageError("invalid history cursor")
		}
		if err := strictJSON(data, &cursor); err != nil {
			return out, autopilotPageError("invalid history cursor")
		}
		if cursor.Project != q.ProjectID || cursor.Task != q.TaskID || cursor.Forward != q.Forward || cursor.Boundary < 0 || cursor.Position < 0 || cursor.Position > cursor.Boundary {
			return out, autopilotPageError("cursor does not match history scope")
		}
		var maximum int64
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM autopilot_audit").Scan(&maximum); err != nil {
			return out, err
		}
		if cursor.Boundary > maximum {
			return out, autopilotPageError("history cursor names a future boundary")
		}
	} else {
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM autopilot_audit").Scan(&cursor.Boundary); err != nil {
			return out, err
		}
		if q.Forward && q.After > cursor.Boundary {
			return out, autopilotPageError("history checkpoint names a future boundary")
		}
		if !q.Forward {
			cursor.Position = cursor.Boundary + 1
		}
	}
	order, comparison := "DESC", "<"
	if q.Forward {
		order, comparison = "ASC", ">"
	}
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("SELECT seq,payload FROM autopilot_audit WHERE seq<=? AND seq%s? AND (?='' OR project_id=?) AND (?='' OR task_id=?) ORDER BY seq %s LIMIT ?", comparison, order), cursor.Boundary, cursor.Position, q.ProjectID, q.ProjectID, q.TaskID, q.TaskID, q.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.Entries = []AutopilotAudit{}
	out.Boundary = cursor.Boundary
	for rows.Next() {
		var a AutopilotAudit
		var data string
		var seq int64
		if err := rows.Scan(&seq, &data); err != nil {
			return AutopilotHistory{}, err
		}
		if err := json.Unmarshal([]byte(data), &a); err != nil {
			return AutopilotHistory{}, err
		}
		a.Sequence = seq
		out.Entries = append(out.Entries, a)
	}
	if err := rows.Err(); err != nil {
		return AutopilotHistory{}, err
	}
	if len(out.Entries) > q.Limit {
		out.Entries = out.Entries[:q.Limit]
		cursor.Position = out.Entries[len(out.Entries)-1].Sequence
		data, _ := json.Marshal(cursor)
		out.Next = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}
