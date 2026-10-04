package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"modernc.org/sqlite"
)

// Execute COMMIT successfully but lose its acknowledgement at the driver
// boundary. This is distinct from discarding an HTTP success response.
type lostCommitDriver struct {
	base driver.Driver
	lose atomic.Bool
}

func (d *lostCommitDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &lostCommitConn{Conn: c, owner: d}, nil
}

type lostCommitConn struct {
	driver.Conn
	owner *lostCommitDriver
}

func (c *lostCommitConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil && query == "COMMIT" && c.owner.lose.Swap(false) {
		return nil, errors.New("injected lost commit acknowledgement")
	}
	return result, err
}

func TestAutopilotLostCommitAcknowledgementRetainsOneEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	s := NewService(store, cfg)
	p := newProject(t, s)
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.Submit(testContext, "lost-commit", "reason", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	d := &lostCommitDriver{base: &sqlite.Driver{}}
	name := "lost-commit-" + uid()
	sql.Register(name, d)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	store.db, err = sql.Open(name, path)
	if err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	d.lose.Store(true)
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	out, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || out.Status != "performed" {
		t.Fatal(out, err)
	}
	v, err := s.Snapshot(testContext)
	if err != nil || v.Projects[0].TitleRevision != p.TitleRevision+1 {
		t.Fatal("committed effect repeated", v, err)
	}
	h, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil || len(h.Entries) != 2 || h.Entries[0].Action.Status != "performed" {
		t.Fatal("commit receipt lost or repeated", h, err)
	}
	mode, _ := cfg.Autopilot.EffectiveMode("authorised-research")
	if mode != autopilot.Suggest {
		t.Fatal("fixture must require owner approval")
	}
}
