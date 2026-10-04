package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
)

func autopilotFixture(t *testing.T, mode autopilot.Mode) (*Service, *AutopilotCoordinator, *AutopilotFunction, Project) {
	t.Helper()
	s, cfg := fixture(t)
	cfg.Autopilot.Modes = map[string]autopilot.Mode{"authorised-research": mode}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return s, c, f, newProject(t, s)
}
func renameProposal(p Project, title string) ConcreteAction {
	args, _ := json.Marshal(RenameProjectArgs{title})
	return ConcreteAction{Kind: "rename-project", ProjectID: p.ID, TargetVersion: p.TitleRevision, PermissionRevision: p.OperatorPermission.Revision, Args: args}
}

func TestAutopilotPendingRestartAndLostApprovalResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	cfg := config.Default()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, cfg)
	p := newProject(t, s)
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	proposal := renameProposal(p, "Changed")
	a, err := f.Submit(testContext, "restart-pending", "reason", proposal)
	if err != nil || a.Status != "proposed" {
		t.Fatalf("proposal: %+v %v", a, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, cfg)
	c = NewAutopilotCoordinator(s, nil)
	f, err = c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	pending, err := c.Pending(testContext, "", 50)
	if err != nil || len(pending) != 1 || pending[0].ID != a.ID {
		t.Fatalf("pending lost: %+v %v", pending, err)
	}
	replay, err := f.Submit(testContext, "restart-pending", "reason", proposal)
	if err != nil || replay.Status != "proposed" || replay.ID != a.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	// Deliberately discard the successful response, as a disconnected caller
	// would. Retrying the immutable approval must read the committed receipt.
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || receipt.Status != "performed" || receipt.ID != a.ID {
		t.Fatalf("retry: %+v %v", receipt, err)
	}
	v, err := s.Snapshot(testContext)
	if err != nil || v.Projects[0].TitleRevision != p.TitleRevision+1 {
		t.Fatal("lost-response retry repeated effect")
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err != nil || len(history.Entries) != 2 {
		t.Fatalf("receipt repeated: %+v %v", history, err)
	}
}

func TestAutopilotCanonicalArgumentsPreserveExactNumbers(t *testing.T) {
	a, err := canonicalAction(ConcreteAction{Kind: "synthetic", ProjectID: "project", Args: json.RawMessage(`{"version":9007199254740993}`)})
	if err != nil || string(a.Args) != `{"version":9007199254740993}` {
		t.Fatalf("concrete arguments changed: %s %v", a.Args, err)
	}
	if _, err := canonicalAction(ConcreteAction{Kind: "synthetic", ProjectID: "project", Args: json.RawMessage(`{} {}`)}); err == nil {
		t.Fatal("multiple argument objects accepted")
	}
}

func TestAutopilotModeEffectsAndReplay(t *testing.T) {
	for _, mode := range []autopilot.Mode{autopilot.Off, autopilot.Suggest, autopilot.Act} {
		t.Run(string(mode), func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, mode)
			proposal := renameProposal(p, "Changed")
			a, err := f.Submit(testContext, "source", "Clearer title", proposal)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.Submit(testContext, "source", "Clearer title", proposal)
			if err != nil || replay.ID != a.ID {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			snapshot, err := s.Snapshot(testContext)
			if err != nil {
				t.Fatal(err)
			}
			history, err := c.History(testContext, AutopilotHistoryQuery{})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case autopilot.Off:
				if a.ID != "" || snapshot.Projects[0].Title != p.Title || len(history.Entries) != 0 {
					t.Fatal("Off had an effect")
				}
			case autopilot.Suggest:
				if a.Status != "proposed" || snapshot.Projects[0].Title != p.Title || len(history.Entries) != 1 {
					t.Fatal("Suggest did not retain exact proposal")
				}
			case autopilot.Act:
				if a.Status != "performed" || snapshot.Projects[0].Title != "Changed" || len(history.Entries) != 2 || a.Executor == "" {
					t.Fatal("Act effect/receipt missing")
				}
			}
			if mode != autopilot.Off {
				if a.AssistantID == "" || a.AssistantName == "" || a.RuleVersion != "v1" || a.Reason != "Clearer title" {
					t.Fatal("attribution missing")
				}
				if _, err := f.Submit(testContext, "source", "Changed reason", proposal); !errors.Is(err, ErrConflict) {
					t.Fatal("replay replaced immutable content")
				}
				if _, err := f.Submit(testContext, "source", "Clearer title", renameProposal(p, "Other")); !errors.Is(err, ErrConflict) {
					t.Fatal("replay replaced immutable arguments")
				}
			}
		})
	}
}

func TestAutopilotRegistrationRejectsForgedAuthority(t *testing.T) {
	s, _ := fixture(t)
	c := NewAutopilotCoordinator(s, nil)
	for _, f := range c.Catalog() {
		if f.Available {
			t.Fatal("production function enabled")
		}
	}
	policy := func(Snapshot, ConcreteAction) error { return nil }
	for _, args := range []struct {
		id, version string
		kinds       []string
	}{{"unknown", "v1", []string{"rename-project"}}, {"ci-failures", "", []string{"rename-project"}}, {"ci-failures", "v1", []string{"shell"}}, {"ci-failures", "v1", []string{"rename-project", "rename-project"}}} {
		if _, err := c.Register(args.id, args.version, args.kinds, policy); err == nil {
			t.Fatal("invalid registration accepted")
		}
	}
	if _, err := c.Register("ci-failures", "v1", []string{"rename-project"}, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register("ci-failures", "v1", []string{"rename-project"}, policy); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate registration accepted")
	}
	forged := &AutopilotFunction{coordinator: c, id: "ci-failures", version: "v1"}
	if _, err := forged.Submit(testContext, "source", "reason", renameProposal(newProject(t, s), "Changed")); err == nil {
		t.Fatal("forged capability accepted")
	}
}

func TestAutopilotApprovalCancellationAndOverride(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	a, err := f.Submit(testContext, "first", "reason", renameProposal(p, "Suggested"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 2, "approve", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision approved")
	}
	replacement := renameProposal(p, "")
	if _, err := c.OwnerAction(testContext, a.ID, 1, "override", &replacement); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	current, _ := c.Action(testContext, a.ID)
	if current.Status != "proposed" {
		t.Fatal("failed override discarded suggestion")
	}
	replacement = renameProposal(p, "Owner title")
	changed, err := c.OwnerAction(testContext, a.ID, 1, "override", &replacement)
	if err != nil || changed.Executor != "owner" || changed.ID == a.ID {
		t.Fatalf("override: %+v %v", changed, err)
	}
	replay, err := f.Submit(testContext, "first", "reason", renameProposal(p, "Suggested"))
	if err != nil || replay.Status != "cancelled" {
		t.Fatal("replay revived replacement's original")
	}
	approved, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || approved.Status != "cancelled" {
		t.Fatal("old approval replaced owner action")
	}
	snap, _ := s.Snapshot(testContext)
	p = snap.Projects[0]
	a, err = f.Submit(testContext, "second", "reason", renameProposal(p, "Another"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := c.OwnerAction(testContext, a.ID, 1, "cancel", nil)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatal("cancel failed")
	}
	pending, err := c.Pending(testContext, "", 50)
	if err != nil || len(pending) != 0 {
		t.Fatal("cancel still pending")
	}
}

func TestAutopilotCurrentAuthorityRechecked(t *testing.T) {
	for _, change := range []string{"mode", "target", "pause", "project-pause", "closed", "policy", "blocked", "upgrade", "admission"} {
		t.Run(change, func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, autopilot.Suggest)
			a, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Changed"))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "mode":
				cfg := s.configuration()
				cfg.Autopilot.Modes = map[string]autopilot.Mode{f.id: autopilot.Off}
				s.UpdateConfig(cfg)
			case "target":
				s.SetProjectTitle(testContext, p.ID, "Owner changed")
			case "pause":
				s.SetPaused(testContext, true)
			case "project-pause":
				s.SetProjectPaused(testContext, p.ID, true)
			case "closed":
				c.Close()
			case "policy":
				f.version = "v2"
			case "blocked":
				c.SerializeSettings(func() error { return errors.New("save failed") })
			case "upgrade":
				s.store.mu.Lock()
				s.upgradeDraining = true
				s.store.mu.Unlock()
			case "admission":
				c.admission = func() error { return errors.New("no dispatch") }
			}
			out, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
			if err != nil || out.Status != "refused" || out.Detail == "" {
				t.Fatalf("stale approval: %+v %v", out, err)
			}
			snap, _ := s.Snapshot(testContext)
			if snap.Projects[0].Title == "Changed" {
				t.Fatal("refused action mutated target")
			}
		})
	}
}

func TestAutopilotOperatorGateIsIndependent(t *testing.T) {
	s, cfg := fixture(t)
	p := newProject(t, s)
	if p.OperatorPermission.Allowed {
		t.Fatal("permission defaults enabled")
	}
	c := NewAutopilotCoordinator(s, nil)
	f, err := c.Register(autopilot.Operator, "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.Submit(testContext, "off", "reason", renameProposal(p, "Changed"))
	if err != nil || a.ID != "" {
		t.Fatal("operator default is not Off")
	}
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Act}
	s.UpdateConfig(cfg)
	a, err = f.Submit(testContext, "gate-false", "reason", renameProposal(p, "Changed"))
	if err != nil || a.Status != "refused" {
		t.Fatal("Act granted operator authority")
	}
	allowed, err := s.SetOperatorPermission(testContext, p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.Playbook.Release != p.Playbook.Release {
		t.Fatal("gate changed release policy")
	}
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Off}
	s.UpdateConfig(cfg)
	if out, err := f.Submit(testContext, "permitted-but-off", "reason", renameProposal(allowed, "Changed")); err != nil || out.ID != "" {
		t.Fatal("project permission overrode Off")
	}
	cfg.Autopilot.Modes = map[string]autopilot.Mode{autopilot.Operator: autopilot.Suggest}
	s.UpdateConfig(cfg)
	a, err = f.Submit(testContext, "gate-true", "reason", renameProposal(allowed, "Changed"))
	if err != nil || a.Status != "proposed" {
		t.Fatal("permitted suggestion missing")
	}
	revoked, err := s.SetOperatorPermission(testContext, p.ID, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SetOperatorPermission(testContext, p.ID, true, revoked.OperatorPermission.Revision)
	if err != nil {
		t.Fatal(err)
	}
	a, err = c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || a.Status != "refused" {
		t.Fatal("re-enabled gate revived approval")
	}
}

func TestAutopilotUndoAndStableHistory(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Act)
	a, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := c.History(testContext, AutopilotHistoryQuery{Limit: 1})
	if err != nil || page.Next == "" {
		t.Fatal("history paging missing")
	}
	undone, err := c.OwnerAction(testContext, a.ID, 1, "undo", nil)
	if err != nil || undone.Status != "undone" {
		t.Fatalf("undo: %+v %v", undone, err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Projects[0].Title != p.Title || snap.Projects[0].TitleRenamed != p.TitleRenamed {
		t.Fatal("inverse did not restore title and flag")
	}
	second, err := c.History(testContext, AutopilotHistoryQuery{Limit: 1, Cursor: page.Next})
	if err != nil || second.Entries[0].Action.Status != "proposed" {
		t.Fatal("later transition shifted older page")
	}
	forward, err := c.History(testContext, AutopilotHistoryQuery{Forward: true})
	if err != nil || len(forward.Entries) != 3 || forward.Entries[2].Action.Status != "undone" {
		t.Fatal("forward history missing transitions")
	}
	if _, err := c.History(testContext, AutopilotHistoryQuery{Cursor: page.Next, Forward: true}); err == nil {
		t.Fatal("wrong cursor scope accepted")
	}
	if _, err := c.History(testContext, AutopilotHistoryQuery{Limit: 201}); err == nil {
		t.Fatal("unbounded history accepted")
	}
	c.OwnerAction(testContext, a.ID, 1, "undo", nil)
	history, _ := c.History(testContext, AutopilotHistoryQuery{})
	if len(history.Entries) != 3 {
		t.Fatal("duplicate undo created another effect")
	}
	snap, _ = s.Snapshot(testContext)
	b, err := f.Submit(testContext, "second", "reason", renameProposal(snap.Projects[0], "Second"))
	if err != nil {
		t.Fatal(err)
	}
	s.SetProjectTitle(testContext, p.ID, "Third")
	if _, err := c.OwnerAction(testContext, b.ID, 1, "undo", nil); !errors.Is(err, ErrConflict) {
		t.Fatal("undo overwrote newer owner title")
	}
}

func TestAutopilotIndependentStoresRaceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	cfg := config.Default()
	s := NewService(st, cfg)
	s2 := NewService(other, cfg)
	p := newProject(t, s)
	c := NewAutopilotCoordinator(s, nil)
	c2 := NewAutopilotCoordinator(s2, nil)
	policy := func(Snapshot, ConcreteAction) error { return nil }
	f, _ := c.Register("authorised-research", "v1", []string{"rename-project"}, policy)
	f2, _ := c2.Register("authorised-research", "v1", []string{"rename-project"}, policy)
	var wg sync.WaitGroup
	results := make(chan AutopilotAction, 2)
	for _, f := range []*AutopilotFunction{f, f2} {
		wg.Add(1)
		go func(f *AutopilotFunction) {
			defer wg.Done()
			a, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Changed"))
			if err != nil {
				t.Error(err)
			}
			results <- a
		}(f)
	}
	wg.Wait()
	close(results)
	id := ""
	for a := range results {
		if id != "" && id != a.ID {
			t.Fatal("duplicate proposal")
		}
		id = a.ID
	}
	for _, coordinator := range []*AutopilotCoordinator{c, c2} {
		wg.Add(1)
		go func(c *AutopilotCoordinator) {
			defer wg.Done()
			if _, err := c.OwnerAction(testContext, id, 1, "approve", nil); err != nil {
				t.Error(err)
			}
		}(coordinator)
	}
	wg.Wait()
	snap, _ := s.Snapshot(testContext)
	if snap.Projects[0].TitleRevision != 1 {
		t.Fatal("double effect")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered := NewAutopilotCoordinator(NewService(reopened, cfg), nil)
	action, err := recovered.Action(testContext, id)
	if err != nil || action.Status != "performed" || action.Approver != "owner" {
		t.Fatal("lost receipt at restart")
	}
	history, err := recovered.History(testContext, AutopilotHistoryQuery{})
	if err != nil || len(history.Entries) != 2 {
		t.Fatal("duplicate/missing durable transitions")
	}
	// An unavailable implementation after restart cannot approve a new action.
	snap, _ = s.Snapshot(testContext)
	pending, err := f.Submit(testContext, "pending", "reason", renameProposal(snap.Projects[0], "Later"))
	if err != nil {
		t.Fatal(err)
	}
	refused, err := recovered.OwnerAction(testContext, pending.ID, 1, "approve", nil)
	if err != nil || refused.Status != "refused" {
		t.Fatal("unavailable implementation executed")
	}
}

func TestAutopilotAuditFailureRollsBackEffect(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	a, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.store.db.Exec(`CREATE TRIGGER fail_autopilot_audit BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil); err == nil {
		t.Fatal("audit failure reported success")
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Projects[0].Title != p.Title {
		t.Fatal("effect committed without audit")
	}
	current, _ := c.Action(testContext, a.ID)
	if current.Status != "proposed" {
		t.Fatal("false receipt after failure")
	}
	if _, err := f.Submit(testContext, "new-source", "reason", renameProposal(p, "Changed")); err == nil {
		t.Fatal("proposal failure reported success")
	}
	var retained int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM autopilot_actions WHERE source=?", "new-source").Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 0 {
		t.Fatal("failed proposal retained")
	}
	var transitions int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM autopilot_audit").Scan(&transitions); err != nil || transitions != 1 {
		t.Fatalf("failed proposal retained audit: %d, %v", transitions, err)
	}
	_, err = s.store.db.Exec("DROP TRIGGER fail_autopilot_audit")
	if err != nil {
		t.Fatal(err)
	}
	performed, err := c.OwnerAction(testContext, a.ID, 1, "approve", nil)
	if err != nil || performed.Status != "performed" {
		t.Fatal("safe retry failed")
	}
}

func TestAutopilotFailedLocalActionHasNoEffect(t *testing.T) {
	s, c, _, p := autopilotFixture(t, autopilot.Act)
	c.RegisterAction("test-failure", LocalAction{Check: func(Snapshot, ConcreteAction) error { return nil }, Apply: func(v *Snapshot, _ ConcreteAction, _ AutopilotActorKind) (json.RawMessage, uint64, error) {
		v.Projects[0].Title = "partial"
		return nil, 0, errors.New("injected failure")
	}})
	f, err := c.Register("ci-failures", "v1", []string{"test-failure"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.configuration()
	cfg.Autopilot.Modes = map[string]autopilot.Mode{"ci-failures": autopilot.Act}
	s.UpdateConfig(cfg)
	a := renameProposal(p, "Changed")
	a.Kind = "test-failure"
	out, err := f.Submit(context.Background(), "source", "reason", a)
	if err != nil || out.Status != "failed" {
		t.Fatalf("failure record: %+v %v", out, err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Projects[0].Title != p.Title {
		t.Fatal("partial failed mutation committed")
	}
}

func TestAutopilotHistoryRejectsFutureCheckpoint(t *testing.T) {
	_, c, f, p := autopilotFixture(t, autopilot.Suggest)
	if _, err := f.Submit(testContext, "history-boundary", "reason", renameProposal(p, "Changed")); err != nil {
		t.Fatal(err)
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{Forward: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.History(testContext, AutopilotHistoryQuery{Forward: true, After: history.Boundary + 1}); err == nil {
		t.Fatal("future checkpoint accepted")
	}
	page, err := c.History(testContext, AutopilotHistoryQuery{Forward: true, After: history.Boundary})
	if err != nil || page.Boundary != history.Boundary || len(page.Entries) != 0 {
		t.Fatalf("current checkpoint: %+v %v", page, err)
	}
}

func TestAutopilotUndoAuditFailureRollsBackInverse(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Act)
	a, err := f.Submit(testContext, "undo-failure", "reason", renameProposal(p, "Changed"))
	if err != nil || a.Status != "performed" {
		t.Fatalf("perform: %+v %v", a, err)
	}
	if _, err := s.store.db.Exec(`CREATE TRIGGER fail_undo_audit BEFORE INSERT ON autopilot_audit BEGIN SELECT RAISE(ABORT,'injected inverse audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(testContext, a.ID, 1, "undo", nil); err == nil {
		t.Fatal("failed inverse reported success")
	}
	v, err := s.Snapshot(testContext)
	if err != nil || v.Projects[0].Title != "Changed" {
		t.Fatalf("inverse escaped rollback: %+v %v", v.Projects, err)
	}
	current, err := c.Action(testContext, a.ID)
	if err != nil || current.Status != "performed" {
		t.Fatalf("false inverse receipt: %+v %v", current, err)
	}
	if _, err := s.store.db.Exec("DROP TRIGGER fail_undo_audit"); err != nil {
		t.Fatal(err)
	}
	undone, err := c.OwnerAction(testContext, a.ID, 1, "undo", nil)
	if err != nil || undone.Status != "undone" {
		t.Fatalf("inverse retry: %+v %v", undone, err)
	}
}

func TestAutopilotHistoryReadFailureIsNotPartialSuccess(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	if _, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Changed")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("DROP TABLE autopilot_audit"); err != nil {
		t.Fatal(err)
	}
	history, err := c.History(testContext, AutopilotHistoryQuery{})
	if err == nil || len(history.Entries) != 0 {
		t.Fatal("database failure returned successful partial history")
	}
}

func TestAutopilotApprovalRacesOwnerCancellationAndReplacement(t *testing.T) {
	for _, verb := range []string{"cancel", "override"} {
		t.Run(verb, func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, autopilot.Suggest)
			a, err := f.Submit(testContext, "source", "reason", renameProposal(p, "Suggested"))
			if err != nil {
				t.Fatal(err)
			}
			replacement := renameProposal(p, "Owner replacement")
			var wg sync.WaitGroup
			for _, action := range []string{"approve", verb} {
				wg.Add(1)
				go func(action string) {
					defer wg.Done()
					var input *ConcreteAction
					if action == "override" {
						input = &replacement
					}
					_, err := c.OwnerAction(testContext, a.ID, 1, action, input)
					if err != nil && !errors.Is(err, ErrConflict) {
						t.Error(err)
					}
				}(action)
			}
			wg.Wait()
			snapshot, _ := s.Snapshot(testContext)
			if snapshot.Projects[0].TitleRevision > 1 {
				t.Fatal("owner race executed twice")
			}
			history, err := c.History(testContext, AutopilotHistoryQuery{})
			if err != nil {
				t.Fatal(err)
			}
			performed := 0
			for _, entry := range history.Entries {
				if entry.Action.Status == "performed" {
					performed++
				}
			}
			if performed > 1 {
				t.Fatal("owner race has two success receipts")
			}
		})
	}
}
