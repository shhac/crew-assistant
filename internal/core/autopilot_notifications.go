package core

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BeginNotification reserves one automatic attempt. A single durable row per
// channel bounds outage state, including when newer audit entries arrive.
// Reserving before send also backs off an interrupted attempt across restart.
func (c *AutopilotCoordinator) BeginNotification(ctx context.Context, key string, now time.Time) (bool, error) {
	ready := false
	err := c.service.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		var attempts int
		var retry string
		var sent bool
		err := conn.QueryRowContext(ctx, `SELECT attempts,retry_at,sent FROM autopilot_notifications WHERE key=?`, key).Scan(&attempts, &retry, &sent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if key != "actions" && sent {
			return nil
		}
		if retry != "" {
			at, err := time.Parse(time.RFC3339Nano, retry)
			if err != nil {
				return err
			}
			if now.Before(at) {
				return nil
			}
		}
		attempts = min(attempts+1, 6)
		next := now.Add(min(time.Duration(1<<attempts)*time.Minute, time.Hour))
		_, err = conn.ExecContext(ctx, `INSERT INTO autopilot_notifications(key,attempts,retry_at,sent) VALUES(?,?,?,0) ON CONFLICT(key) DO UPDATE SET attempts=excluded.attempts,retry_at=excluded.retry_at,sent=0`, key, attempts, next.UTC().Format(time.RFC3339Nano))
		ready = err == nil
		return err
	})
	return ready, err
}

// FinishNotification records the successful presentation and action cursor in
// one commit. Failures retain the reservation and record one outage activity,
// rather than creating interrupted-operation claims on every automatic retry.
func (c *AutopilotCoordinator) FinishNotification(ctx context.Context, key string, boundary int64, success bool) error {
	if !success {
		var reported bool
		if err := c.service.store.db.QueryRowContext(ctx, `SELECT reported FROM autopilot_notifications WHERE key=?`, key).Scan(&reported); err != nil {
			return err
		}
		if reported {
			return nil
		}
		return c.service.store.updateTransaction(ctx, func(v *Snapshot, conn *sql.Conn) error {
			result, err := conn.ExecContext(ctx, `UPDATE autopilot_notifications SET reported=1 WHERE key=? AND reported=0`, key)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed > 0 {
				record(v, c.service.now().UTC(), "", "autopilot.notification.failed", "Autopilot notification failed; automatic retries back off to once an hour")
			}
			return nil
		})
	}
	return c.service.store.updateAutopilotEvents(ctx, func(conn *sql.Conn) error {
		if key == "actions" {
			if err := advanceAutopilotProgress(ctx, conn, "notified", boundary); err != nil {
				return err
			}
		}
		_, err := conn.ExecContext(ctx, `UPDATE autopilot_notifications SET attempts=0,retry_at='',sent=1,reported=0 WHERE key=?`, key)
		return err
	})
}
