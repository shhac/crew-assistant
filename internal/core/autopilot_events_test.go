package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
)

func eventCount(t *testing.T, s *Service, where string) int {
	t.Helper()
	var n int
	if err := s.store.db.QueryRow("SELECT COUNT(*) FROM autopilot_events WHERE " + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAutopilotCaptureAtomicity(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	change := func(v *Snapshot, _ *sql.Conn) error {
		v.Decisions = append(v.Decisions, Decision{ID: "synthetic-choice", ProjectID: p.ID, Status: "open"})
		v.Tasks = append(v.Tasks, Task{ID: "synthetic-land", ProjectID: p.ID, Status: TaskLanded, Revisions: []Revision{{N: 3}}})
		project(v, p.ID).Releases = append(project(v, p.ID).Releases, ReleaseRecord{Version: "v1.2.3"})
		return nil
	}
	sentinel := errors.New("rollback")
	if err := s.store.updateTransaction(t.Context(), func(v *Snapshot, c *sql.Conn) error { change(v, c); return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if eventCount(t, s, "1=1") != 0 {
		t.Fatal("failed state change captured events")
	}
	if err := s.store.updateTransaction(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "1=1") != 3 {
		t.Fatal("missing atomic capture")
	}
	if err := s.store.update(t.Context(), func(v *Snapshot) error { v.Decisions[0].Status = DecisionResolved; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.store.update(t.Context(), func(*Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "1=1") != 4 {
		t.Fatal("duplicate or missing resolution")
	}
	// Capture failure must roll back the state change too.
	if _, err := s.store.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON autopilot_events BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
		t.Fatal(err)
	}
	err := s.store.update(t.Context(), func(v *Snapshot) error {
		v.Decisions = append(v.Decisions, Decision{ID: "cannot-commit", Status: "open"})
		return nil
	})
	if err == nil {
		t.Fatal("capture failure ignored")
	}
	snap, _ := s.Snapshot(t.Context())
	if len(snap.Decisions) != 1 {
		t.Fatal("state committed without event")
	}
}

func TestCIIdentityAndHeartbeatCoalescing(t *testing.T) {
	s, _ := fixture(t)
	e := CIEvent{Provider: "synthetic", Repo: "example/repo", Ref: "main", Commit: "abc", Check: "tests", State: "FAILURE"}
	for i := 0; i < 2; i++ {
		if err := s.RecordCIEvent(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	if eventCount(t, s, "kind='ci.result'") != 1 {
		t.Fatal("CI duplicate")
	}
	e.Check = "lint"
	if err := s.RecordCIEvent(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "kind='ci.result'") != 2 {
		t.Fatal("checks masked one another")
	}
	e.Commit = ""
	if err := s.RecordCIEvent(t.Context(), e); err == nil {
		t.Fatal("missing identity accepted")
	}
	now := time.Date(2026, 10, 4, 12, 1, 0, 0, time.UTC)
	for _, at := range []time.Time{now, now.Add(time.Minute), now.Add(time.Hour)} {
		if err := s.RecordAutopilotHeartbeat(t.Context(), at); err != nil {
			t.Fatal(err)
		}
	}
	if eventCount(t, s, "kind='heartbeat'") != 2 || eventCount(t, s, "kind='heartbeat' AND status='pending'") != 1 {
		t.Fatal("heartbeat did not coalesce")
	}
}

func TestSubmitEventDefersWithoutLosingSuggestion(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	tests := []struct {
		name  string
		close func()
		open  func()
	}{
		{"closed", func() { c.closed = true }, func() { c.closed = false }},
		{"blocked", func() { c.blocked = true }, func() { c.blocked = false }},
		{"upgrading", func() { s.upgradeDraining = true }, func() { s.upgradeDraining = false }},
		{"stopping", func() { c.admission = func() error { return errors.New("stopping") } }, func() { c.admission = nil }},
		{"global pause", func() { s.store.update(t.Context(), func(v *Snapshot) error { v.Paused = true; return nil }) }, func() { s.store.update(t.Context(), func(v *Snapshot) error { v.Paused = false; return nil }) }},
		{"project pause", func() {
			s.store.update(t.Context(), func(v *Snapshot) error { project(v, p.ID).Paused = true; return nil })
		}, func() {
			s.store.update(t.Context(), func(v *Snapshot) error { project(v, p.ID).Paused = false; return nil })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.close()
			_, err := f.SubmitEvent(t.Context(), test.name, "test", renameProposal(p, "Changed"))
			if !errors.Is(err, ErrAutopilotDeferred) {
				t.Fatalf("not deferred: %v", err)
			}
			history, _ := c.History(t.Context(), AutopilotHistoryQuery{})
			for _, a := range history.Entries {
				if a.Action.Source == test.name {
					t.Fatal("deferral wrote refusal")
				}
			}
			test.open()
			a, err := f.SubmitEvent(t.Context(), test.name, "test", renameProposal(p, "Changed"))
			if err != nil || a.Status != "proposed" {
				t.Fatalf("lost suggestion: %+v %v", a, err)
			}
			test.close()
			replay, err := f.SubmitEvent(t.Context(), test.name, "test", renameProposal(p, "Changed"))
			if err != nil || replay.ID != a.ID || replay.Revision != a.Revision {
				t.Fatal("existing suggestion changed")
			}
			test.open()
		})
	}
	a, err := f.SubmitEvent(t.Context(), "bad-target", "test", renameProposal(Project{ID: "missing"}, "Changed"))
	if err != nil || a.Status != "refused" {
		t.Fatalf("permanent refusal: %+v %v", a, err)
	}
}

func TestEventReplayRecoveryAndStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	cfg := config.Default()
	cfg.Autopilot.Modes = map[string]autopilot.Mode{"authorised-research": autopilot.Act}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, cfg)
	p := newProject(t, s)
	c := NewAutopilotCoordinator(s, nil)
	now := time.Now()
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	proposal := renameProposal(p, "Changed")
	register := func(c *AutopilotCoordinator) *AutopilotFunction {
		f, err := c.Register("authorised-research", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		if err := f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
			return []EventProposal{{Key: "rename", Reason: "test", Action: proposal}}, nil
		}); err != nil {
			t.Fatal(err)
		}
		return f
	}
	register(c)
	// Lose delivery marking after a committed effect.
	if _, err := store.db.Exec(`CREATE TRIGGER fail_delivery BEFORE INSERT ON autopilot_event_deliveries BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err == nil {
		t.Fatal("delivery storage failure ignored")
	}
	history, _ := c.History(t.Context(), AutopilotHistoryQuery{})
	if len(history.Entries) != 2 {
		t.Fatal("effect not committed before failed delivery mark")
	}
	store.db.Exec(`DROP TRIGGER fail_delivery`)
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, cfg)
	c = NewAutopilotCoordinator(s, nil)
	register(c)
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "status='pending'") != 1 {
		t.Fatal("stopping consumed work")
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	history, _ = c.History(t.Context(), AutopilotHistoryQuery{})
	if len(history.Entries) != 2 {
		t.Fatal("restart repeated effect or audit")
	}
	snap, _ := s.Snapshot(t.Context())
	if snap.Projects[0].TitleRevision != p.TitleRevision+1 {
		t.Fatal("replayed effect")
	}
	if eventCount(t, s, "status='pending'") != 0 {
		t.Fatal("restart lost delivery")
	}
}

func TestEventRetryBackoffAndBoundedCatchup(t *testing.T) {
	s, c, f, _ := autopilotFixture(t, autopilot.Suggest)
	now := time.Now()
	f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
		return nil, errors.New("synthetic handler")
	})
	s.RecordAutopilotHeartbeat(t.Context(), now)
	for i := 0; i < 5; i++ {
		if err := c.ConsumeEvents(t.Context(), now.Add(time.Duration(i)*time.Hour), func() bool { return false }); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var attempts int
	if err := s.store.db.QueryRow(`SELECT status,attempts FROM autopilot_event_deliveries`).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != 5 {
		t.Fatalf("retry boundary %s %d", status, attempts)
	}
	s.store.updateTransaction(t.Context(), func(_ *Snapshot, conn *sql.Conn) error {
		for i := 0; i < 201; i++ {
			if err := insertAutopilotEvent(t.Context(), conn, AutopilotEvent{ID: fmt.Sprintf("old:%03d", i), Kind: "synthetic", OccurredAt: now.Add(-8 * 24 * time.Hour)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "status='expired'") != 200 {
		t.Fatal("catchup batch exceeded 200")
	}
}

func TestSummaryAcknowledgementRacesAndOutcomes(t *testing.T) {
	_, c, f, p := autopilotFixture(t, autopilot.Suggest)
	first, err := f.Submit(t.Context(), "one", "test", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := c.UnseenSummary(t.Context())
	if err != nil || len(summary.Groups["proposed"]) != 1 {
		t.Fatalf("summary %+v %v", summary, err)
	}
	seen, _ := c.Progress(t.Context(), "owner_seen")
	if seen != 0 {
		t.Fatal("query acknowledged presentation")
	}
	c.OwnerAction(t.Context(), first.ID, 1, "cancel", nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.AcknowledgeSummary(t.Context(), int64(i%2)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	seen, _ = c.Progress(t.Context(), "owner_seen")
	if seen != summary.Boundary {
		t.Fatal("acknowledgements regressed")
	}
	newer, err := c.UnseenSummary(t.Context())
	if err != nil || len(newer.Groups["cancelled"]) != 1 || newer.Boundary <= seen {
		t.Fatal("newer entries acknowledged by stale presentation")
	}
	if err := c.AcknowledgeSummary(t.Context(), 100); !errors.Is(err, ErrAutopilotPage) {
		t.Fatal("future ack accepted")
	}
}

func TestDigestOptInLocalDatesAndRestart(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Europe/London"} {
		t.Run(zone, func(t *testing.T) {
			s, c, _, _ := autopilotFixture(t, autopilot.Suggest)
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 1, 8, 0, 0, 0, loc)
			if d, err := c.RecordDigest(t.Context(), now, loc); err != nil || d != nil {
				t.Fatal("default digest enabled")
			}
			cfg := s.configuration()
			cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "08:00"}
			s.UpdateConfig(cfg)
			if d, err := c.RecordDigest(t.Context(), now.Add(-time.Minute), loc); err != nil || d != nil {
				t.Fatal("digest before time")
			}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.RecordDigest(t.Context(), now, loc); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			reopened := NewAutopilotCoordinator(s, nil)
			if d, err := reopened.RecordDigest(t.Context(), now, loc); err != nil || d != nil {
				t.Fatal("restart duplicate digest")
			}
			c.RecordDigest(t.Context(), now.AddDate(0, 0, 4), loc)
			ds, err := c.Digests(t.Context(), "", 50)
			if err != nil || len(ds) != 2 {
				t.Fatal("digest backfill or duplicate")
			}
			if d, err := c.RecordDigest(t.Context(), now.Add(-24*time.Hour), loc); err != nil {
				t.Fatal(err)
			} else if d == nil {
				t.Fatal("earlier unseen calendar day not recorded")
			}
			if d, err := c.RecordDigest(t.Context(), now, loc); err != nil || d != nil {
				t.Fatal("clock rollback duplicated date")
			}
		})
	}
}

func TestEventCurrentModeAndAuthority(t *testing.T) {
	for _, mode := range []autopilot.Mode{autopilot.Off, autopilot.Suggest, autopilot.Act} {
		t.Run(string(mode), func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, autopilot.Act)
			now := time.Now()
			s.RecordAutopilotHeartbeat(t.Context(), now)
			f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
				return []EventProposal{{Key: "rename", Reason: "test", Action: renameProposal(p, "Changed")}}, nil
			})
			cfg := s.configuration()
			cfg.Autopilot.Modes[f.id] = mode
			s.UpdateConfig(cfg)
			if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
				t.Fatal(err)
			}
			history, err := c.History(t.Context(), AutopilotHistoryQuery{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == autopilot.Off {
				var detail string
				if err := s.store.db.QueryRow(`SELECT detail FROM autopilot_event_deliveries`).Scan(&detail); err != nil || !strings.Contains(detail, "Off") {
					t.Fatal("Off delivery lacks no-op detail")
				}
			}
			expected := map[autopilot.Mode]int{autopilot.Off: 0, autopilot.Suggest: 1, autopilot.Act: 2}[mode]
			if len(history.Entries) != expected {
				t.Fatal("delivery used capture-time mode")
			}
		})
	}
	s, c, _, p := autopilotFixture(t, autopilot.Act)
	f, err := c.Register(autopilot.Operator, "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.SetOperatorPermission(t.Context(), p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	action := renameProposal(p, "Changed")
	cfg := s.configuration()
	cfg.Autopilot.Modes[autopilot.Operator] = autopilot.Act
	s.UpdateConfig(cfg)
	now := time.Now()
	s.RecordAutopilotHeartbeat(t.Context(), now)
	s.SetOperatorPermission(t.Context(), p.ID, false, p.OperatorPermission.Revision)
	f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
		return []EventProposal{{Key: "rename", Reason: "test", Action: action}}, nil
	})
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	history, _ := c.History(t.Context(), AutopilotHistoryQuery{})
	if len(history.Entries) != 1 || history.Entries[0].Action.Status != "refused" {
		t.Fatal("revoked authority used")
	}
}

func TestStopDuringEventConsumptionAndForceCancellation(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, autopilot.Suggest)
			now := time.Now()
			s.store.updateTransaction(t.Context(), func(_ *Snapshot, conn *sql.Conn) error {
				for i := 0; i < 2; i++ {
					if err := insertAutopilotEvent(t.Context(), conn, AutopilotEvent{ID: fmt.Sprint(i), Kind: "synthetic", OccurredAt: now}); err != nil {
						return err
					}
				}
				return nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopped := false
			calls := 0
			f.OnEvents([]string{"synthetic"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
				calls++
				stopped = true
				if force {
					cancel()
				}
				return []EventProposal{{Key: "rename", Reason: "test", Action: renameProposal(p, "Changed")}}, nil
			})
			c.ConsumeEvents(ctx, now, func() bool { return stopped })
			if calls != 1 {
				t.Fatal("new event started after stop")
			}
			expected := 1
			if force {
				expected = 2
			}
			if eventCount(t, s, "status='pending'") != expected {
				t.Fatal("stop lost pending event")
			}
			history, _ := c.History(t.Context(), AutopilotHistoryQuery{})
			if force && len(history.Entries) != 0 {
				t.Fatal("force cancellation committed a proposal")
			}
		})
	}
}

func TestSummaryFixedBoundaryAllOutcomesAndTruncation(t *testing.T) {
	s, c, _, p := autopilotFixture(t, autopilot.Suggest)
	if err := s.store.updateTransaction(t.Context(), func(v *Snapshot, conn *sql.Conn) error {
		v.Decisions = append(v.Decisions, Decision{ID: "needs-owner", ProjectID: p.ID, Status: "open", Title: "Choose"})
		for _, status := range []string{"proposed", "performed", "failed", "uncertain", "refused", "undone", "cancelled"} {
			a := AutopilotAction{ID: status, Source: status, Function: "authorised-research", Status: status, Action: ConcreteAction{ProjectID: p.ID}}
			if err := writeAutopilotAction(t.Context(), conn, a, "synthetic", AutopilotAssistant, time.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := c.Summary(t.Context(), SummaryQuery{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text(), "uncertain:") {
		t.Fatal("unconfirmed effect presented as failed")
	}
	if summary.Counts["failed"] != 2 || summary.Counts["needs_owner"] != 2 {
		t.Fatalf("outcomes %+v", summary.Counts)
	}
	for _, group := range []string{"proposed", "performed", "refused", "undone", "cancelled"} {
		if summary.Counts[group] != 1 {
			t.Fatalf("missing %s", group)
		}
	}
	if err := s.store.updateTransaction(t.Context(), func(_ *Snapshot, conn *sql.Conn) error {
		for i := 0; i < 1005; i++ {
			a := AutopilotAction{ID: fmt.Sprint(i), Source: fmt.Sprint(i), Status: "performed", Action: ConcreteAction{ProjectID: p.ID}}
			if err := writeAutopilotAction(t.Context(), conn, a, "synthetic", AutopilotAssistant, time.Now()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old, err := c.Summary(t.Context(), SummaryQuery{Boundary: summary.Boundary, FixedBoundary: true})
	if err != nil || old.Counts["performed"] != 1 {
		t.Fatal("fixed boundary changed")
	}
	truncated, err := c.Summary(t.Context(), SummaryQuery{})
	if err != nil || !truncated.Truncated || truncated.Boundary != 1000 {
		t.Fatalf("unbounded summary %+v %v", truncated, err)
	}
	if err := c.AcknowledgeSummary(t.Context(), truncated.Boundary); err != nil {
		t.Fatal(err)
	}
	remainder, err := c.UnseenSummary(t.Context())
	if err != nil || remainder.Boundary <= truncated.Boundary {
		t.Fatal("truncation acknowledged unseen audit")
	}
}

func TestEventConflictAndNudgesCoalesce(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	now := time.Now()
	s.RecordAutopilotHeartbeat(t.Context(), now)
	proposal := renameProposal(p, "First")
	extra := false
	f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) {
		proposals := []EventProposal{{Key: "rename", Reason: "test", Action: proposal}}
		if extra {
			proposals = append(proposals, EventProposal{Key: "independent", Reason: "later key", Action: renameProposal(p, "Independent")})
		}
		return proposals, nil
	})
	s.store.db.Exec(`CREATE TRIGGER reject_delivery BEFORE INSERT ON autopilot_event_deliveries BEGIN SELECT RAISE(ABORT,'synthetic'); END`)
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err == nil {
		t.Fatal("storage failure ignored")
	}
	s.store.db.Exec(`DROP TRIGGER reject_delivery`)
	proposal = renameProposal(p, "Different")
	extra = true
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := s.store.db.QueryRow(`SELECT status FROM autopilot_event_deliveries`).Scan(&status); err != nil || status != "conflict" {
		t.Fatal("changed replay did not conflict")
	}
	pending, _ := c.Pending(t.Context(), "", 50)
	if len(pending) != 2 {
		t.Fatal("conflict lost later independent key")
	}
	for _, a := range pending {
		if string(a.Action.Args) == string(proposal.Args) {
			t.Fatal("conflict overwrote pending suggestion")
		}
	}
	for i := 0; i < 5; i++ {
		s.RecordCIEvent(t.Context(), CIEvent{Provider: "synthetic", Repo: "r", Ref: "main", Commit: fmt.Sprint(i), Check: "tests", State: "SUCCESS"})
	}
	if len(s.store.autopilotNudge) != 1 {
		t.Fatal("nudges did not coalesce")
	}
}

func TestDigestLocalMidnightAndDSTCalendarBoundary(t *testing.T) {
	for _, zone := range []string{"America/New_York", "Europe/London"} {
		t.Run(zone, func(t *testing.T) {
			s, c, _, _ := autopilotFixture(t, autopilot.Suggest)
			cfg := s.configuration()
			cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "00:00"}
			s.UpdateConfig(cfg)
			loc, err := time.LoadLocation(zone)
			if err != nil {
				t.Fatal(err)
			}
			day := 25
			month := time.October
			if zone == "America/New_York" {
				day = 1
				month = time.November
			}
			midnight := time.Date(2026, month, day, 0, 0, 0, 0, loc)
			for _, at := range []time.Time{midnight.Add(-time.Second), midnight, midnight.Add(2 * time.Hour), midnight.Add(3 * time.Hour), midnight.AddDate(0, 0, 1)} {
				if _, err := c.RecordDigest(t.Context(), at, loc); err != nil {
					t.Fatal(err)
				}
			}
			ds, err := c.Digests(t.Context(), "", 50)
			if err != nil || len(ds) != 3 {
				t.Fatalf("DST/local midnight duplicated dates: %+v %v", ds, err)
			}
		})
	}
}

func TestPauseHoldsEventsEvenWithoutRegisteredFunctions(t *testing.T) {
	s, _ := fixture(t)
	c := NewAutopilotCoordinator(s, nil)
	now := time.Now()
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "status='pending'") != 1 {
		t.Fatal("pause discarded pending event")
	}
	if err := s.SetPaused(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "status='pending'") != 0 {
		t.Fatal("reopening did not settle event")
	}
	var deliveries int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM autopilot_event_deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatal("unavailable function received delivery")
	}
}

func TestEventAdmissionClosureDuringRevalidationRollsBack(t *testing.T) {
	for _, closeOn := range []int{2, 3} {
		t.Run(fmt.Sprint(closeOn), func(t *testing.T) {
			_, c, f, p := autopilotFixture(t, autopilot.Act)
			checks := 0
			c.admission = func() error {
				checks++
				if checks >= closeOn {
					return errors.New("stopping")
				}
				return nil
			}
			if _, err := f.SubmitEvent(t.Context(), "race", "test", renameProposal(p, "Changed")); !errors.Is(err, ErrAutopilotDeferred) {
				t.Fatalf("closure became a permanent refusal: %v", err)
			}
			history, err := c.History(t.Context(), AutopilotHistoryQuery{})
			if err != nil || len(history.Entries) != 0 {
				t.Fatal("temporary closure left a proposal or refusal")
			}
			c.admission = nil
			a, err := f.SubmitEvent(t.Context(), "race", "test", renameProposal(p, "Changed"))
			if err != nil || a.Status != "performed" {
				t.Fatal("reopening lost pending work")
			}
		})
	}
}

func TestEventChronologicalOrderAcrossWholeSecondBoundary(t *testing.T) {
	s, c, f, _ := autopilotFixture(t, autopilot.Suggest)
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.store.updateTransaction(t.Context(), func(_ *Snapshot, conn *sql.Conn) error {
		for _, e := range []AutopilotEvent{{ID: "later", Kind: "synthetic", OccurredAt: now.Add(time.Nanosecond)}, {ID: "earlier", Kind: "synthetic", OccurredAt: now}} {
			if err := insertAutopilotEvent(t.Context(), conn, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	seen := []string{}
	f.OnEvents([]string{"synthetic"}, func(_ context.Context, _ Snapshot, e AutopilotEvent) ([]EventProposal, error) {
		seen = append(seen, e.ID)
		return nil, nil
	})
	if err := c.ConsumeEvents(t.Context(), now.Add(time.Second), func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "earlier,later" {
		t.Fatalf("events not oldest first: %v", seen)
	}
}

func TestOperatorEventLandingPauseDefersWithoutRefusal(t *testing.T) {
	s, c, _, p := autopilotFixture(t, autopilot.Suggest)
	f, err := c.Register(autopilot.Operator, "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.SetOperatorPermission(t.Context(), p.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.configuration()
	cfg.Autopilot.Modes[autopilot.Operator] = autopilot.Suggest
	s.UpdateConfig(cfg)
	if _, err := s.SetLandingPaused(t.Context(), p.ID, true, "synthetic freeze"); err != nil {
		t.Fatal(err)
	}
	action := renameProposal(p, "Changed")
	if _, err := f.SubmitEvent(t.Context(), "landing-paused", "test", action); !errors.Is(err, ErrAutopilotDeferred) {
		t.Fatal("landing pause lost event")
	}
	history, _ := c.History(t.Context(), AutopilotHistoryQuery{})
	if len(history.Entries) != 0 {
		t.Fatal("temporary landing pause wrote refusal")
	}
	if _, err := s.SetLandingPaused(t.Context(), p.ID, false, ""); err != nil {
		t.Fatal(err)
	}
	proposal, err := f.SubmitEvent(t.Context(), "landing-paused", "test", action)
	if err != nil || proposal.Status != "proposed" {
		t.Fatal("resume lost suggestion")
	}
}

func TestSummaryTextIsCompactWithoutDiscardingHistory(t *testing.T) {
	summary := AutopilotSummary{Groups: map[string][]AutopilotAction{}}
	for i := 0; i < 100; i++ {
		summary.Groups["performed"] = append(summary.Groups["performed"], AutopilotAction{ID: fmt.Sprint(i), Status: "performed", Reason: strings.Repeat("long reason ", 1000), Detail: strings.Repeat("long detail ", 1000)})
	}
	rendered := summary.Text()
	if len(rendered) > 2000 || !strings.Contains(rendered, "100 performed") || !strings.Contains(rendered, "97 more in history") {
		t.Fatal("text presentation is not compact")
	}
	if len(summary.Groups["performed"]) != 100 {
		t.Fatal("presentation discarded history")
	}
}

func TestHeldEventsCannotStarveNewerProjects(t *testing.T) {
	for _, backoff := range []bool{false, true} {
		t.Run(fmt.Sprint(backoff), func(t *testing.T) {
			s, c, f, p := autopilotFixture(t, autopilot.Suggest)
			now := time.Now().UTC()
			if err := s.store.updateTransaction(t.Context(), func(v *Snapshot, conn *sql.Conn) error {
				project(v, p.ID).Paused = !backoff
				for i := 0; i < 201; i++ {
					e := AutopilotEvent{ID: fmt.Sprintf("held:%03d", i), Kind: "synthetic", ProjectID: p.ID, OccurredAt: now.Add(-time.Hour)}
					if err := insertAutopilotEvent(t.Context(), conn, e); err != nil {
						return err
					}
					if backoff {
						if _, err := conn.ExecContext(t.Context(), `INSERT INTO autopilot_event_deliveries VALUES(?,?,'pending',1,?,'retry')`, e.ID, f.id, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
							return err
						}
					}
				}
				return insertAutopilotEvent(t.Context(), conn, AutopilotEvent{ID: "ready", Kind: "synthetic", OccurredAt: now})
			}); err != nil {
				t.Fatal(err)
			}
			seen := []string{}
			f.OnEvents([]string{"synthetic"}, func(_ context.Context, _ Snapshot, e AutopilotEvent) ([]EventProposal, error) {
				seen = append(seen, e.ID)
				return nil, nil
			})
			for i := 0; i < 2; i++ {
				if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Join(seen, ",") != "ready" {
				t.Fatalf("held rows starved ready event: %v", seen)
			}
			if eventCount(t, s, "status='pending'") != 201 {
				t.Fatal("held events were discarded")
			}
		})
	}
}

func TestHeartbeatNoopAvoidsStateWritesAndBoundsRetention(t *testing.T) {
	s, _ := fixture(t)
	now := time.Now().UTC().Truncate(30 * time.Minute)
	var before, after int
	if err := s.store.db.QueryRow("SELECT version FROM state WHERE id=1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	<-s.AutopilotNudges()
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if len(s.store.autopilotNudge) != 0 {
		t.Fatal("duplicate heartbeat nudged")
	}
	if err := s.store.db.QueryRow("SELECT version FROM state WHERE id=1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("heartbeat rewrote state")
	}
	later := now.Add(8 * 24 * time.Hour)
	if err := s.RecordAutopilotHeartbeat(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "kind='heartbeat'") != 1 {
		t.Fatal("old heartbeats retained")
	}
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "kind='heartbeat'") != 1 {
		t.Fatal("pruned heartbeat replayed")
	}
}

func TestAutopilotTableOnlyWritesDoNotRewriteState(t *testing.T) {
	s, c, f, _ := autopilotFixture(t, autopilot.Suggest)
	now := time.Now()
	cfg := s.configuration()
	cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "00:00"}
	s.UpdateConfig(cfg)
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := f.OnEvents([]string{"heartbeat"}, func(context.Context, Snapshot, AutopilotEvent) ([]EventProposal, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	// Any accidental project-state write fails, even if it would serialize an
	// identical payload. Event receipts, cursors and duplicate digests need none.
	if _, err := s.store.db.Exec(`CREATE TRIGGER no_state_write BEFORE UPDATE ON state BEGIN SELECT RAISE(ABORT,'unexpected state write'); END`); err != nil {
		t.Fatal(err)
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if eventCount(t, s, "status='delivered'") != 1 {
		t.Fatal("event not completed")
	}
	if d, err := c.RecordDigest(t.Context(), now, time.UTC); err != nil || d == nil {
		t.Fatal("digest insert", d, err)
	}
	for i := 0; i < 10; i++ {
		if d, err := c.RecordDigest(t.Context(), now, time.UTC); err != nil || d != nil {
			t.Fatal("duplicate digest", d, err)
		}
		if err := c.AcknowledgeSummary(t.Context(), 0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSummaryPendingUsesCurrentActionsButPreservesPastBoundary(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Suggest)
	a, err := f.Submit(t.Context(), "pending-query", "test", renameProposal(p, "Changed"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Summary(t.Context(), SummaryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// With no new audit rows, the pending read must use the indexed action
	// table; it need not decode the proposal's already-seen audit copy again.
	var original string
	if err := s.store.db.QueryRow(`SELECT payload FROM autopilot_audit WHERE seq=1`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec(`UPDATE autopilot_audit SET payload='malformed' WHERE seq=1`); err != nil {
		t.Fatal(err)
	}
	current, err := c.Summary(t.Context(), SummaryQuery{After: first.Boundary})
	if err != nil || current.Counts["proposed"] != 1 {
		t.Fatal("pending query rescanned old audit", current, err)
	}
	if _, err := s.store.db.Exec(`UPDATE autopilot_audit SET payload=? WHERE seq=1`, original); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OwnerAction(t.Context(), a.ID, a.Revision, "cancel", nil); err != nil {
		t.Fatal(err)
	}
	past, err := c.Summary(t.Context(), SummaryQuery{After: first.Boundary, Boundary: first.Boundary, FixedBoundary: true})
	if err != nil || past.Counts["proposed"] != 1 {
		t.Fatal("historical proposal lost", past, err)
	}
	current, err = c.Summary(t.Context(), SummaryQuery{After: first.Boundary})
	if err != nil || current.Counts["proposed"] != 0 || current.Counts["cancelled"] != 1 {
		t.Fatal("current cancelled proposal still pending", current, err)
	}
}

func TestNotificationReservationConcurrentRestartAndReceiptFailure(t *testing.T) {
	s, c, _, _ := autopilotFixture(t, autopilot.Suggest)
	now := time.Now()
	var wg sync.WaitGroup
	ready := make(chan bool, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := c.BeginNotification(t.Context(), "actions", now)
			if err != nil {
				t.Error(err)
			}
			ready <- ok
		}()
	}
	wg.Wait()
	close(ready)
	count := 0
	for ok := range ready {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatal("concurrent send admission", count)
	}
	// A crash while sending leaves a durable backoff, with no owner claim.
	c = NewAutopilotCoordinator(s, nil)
	if ok, err := c.BeginNotification(t.Context(), "actions", now.Add(time.Minute)); err != nil || ok {
		t.Fatal("restart bypassed backoff", ok, err)
	}
	if ok, err := c.BeginNotification(t.Context(), "actions", now.Add(2*time.Minute)); err != nil || !ok {
		t.Fatal("restart failed to retry", ok, err)
	}
	if _, err := s.store.db.Exec(`CREATE TRIGGER reject_receipt BEFORE UPDATE ON autopilot_notifications BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishNotification(t.Context(), "actions", 0, true); err == nil {
		t.Fatal("receipt failure hidden")
	}
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM autopilot_progress WHERE key='notified'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("cursor committed without receipt", n, err)
	}
}

func TestEventHandlersRefreshAfterActionsAndOwnerEdits(t *testing.T) {
	for _, mode := range []autopilot.Mode{autopilot.Act, autopilot.Suggest} {
		for _, ownerEdit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/owner-edit=%t", mode, ownerEdit), func(t *testing.T) {
				s, c, f, p := autopilotFixture(t, mode)
				now := time.Now()
				if err := s.store.updateAutopilotEvents(t.Context(), func(conn *sql.Conn) error {
					for _, id := range []string{"first", "second"} {
						if err := insertAutopilotEvent(t.Context(), conn, AutopilotEvent{ID: id, Kind: "synthetic", ProjectID: p.ID, OccurredAt: now}); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				var seen []uint64
				if err := f.OnEvents([]string{"synthetic"}, func(ctx context.Context, snap Snapshot, event AutopilotEvent) ([]EventProposal, error) {
					current := project(&snap, p.ID)
					seen = append(seen, current.TitleRevision)
					if ownerEdit && event.ID == "first" {
						_, err := s.SetProjectTitle(ctx, p.ID, "Owner edit")
						return nil, err
					}
					return []EventProposal{{Key: "rename", Reason: event.ID, Action: renameProposal(*current, "Changed "+event.ID)}}, nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
					t.Fatal(err)
				}
				want := p.TitleRevision
				if ownerEdit || mode == autopilot.Act {
					want++
				}
				if len(seen) != 2 || seen[1] != want {
					t.Fatalf("stale handler revision: %v want second %d", seen, want)
				}
				history, err := c.History(t.Context(), AutopilotHistoryQuery{})
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range history.Entries {
					if entry.Action.Status == "refused" || entry.Action.Status == "failed" {
						t.Fatal("stale input permanently consumed source", entry.Action)
					}
				}
				pending, err := c.Pending(t.Context(), "", 50)
				if err != nil {
					t.Fatal(err)
				}
				if mode == autopilot.Suggest {
					found := false
					for _, action := range pending {
						if action.Reason == "second" {
							found = true
							if action.Action.TargetVersion != want {
								t.Fatal("suggestion has stale target")
							}
						}
					}
					if !found {
						t.Fatal("second suggestion lost")
					}
				}
			})
		}
	}
}

func TestEventHandlersRefreshBetweenFunctions(t *testing.T) {
	s, c, f, p := autopilotFixture(t, autopilot.Act)
	g, err := c.Register("ci-failures", "v1", []string{"rename-project"}, func(Snapshot, ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.configuration()
	cfg.Autopilot.Modes[g.id] = autopilot.Act
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.RecordAutopilotHeartbeat(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	for _, function := range []*AutopilotFunction{f, g} {
		if err := function.OnEvents([]string{"heartbeat"}, func(_ context.Context, snap Snapshot, _ AutopilotEvent) ([]EventProposal, error) {
			current := project(&snap, p.ID)
			return []EventProposal{{Key: "rename", Reason: "test", Action: renameProposal(*current, fmt.Sprintf("Revision %d", current.TitleRevision))}}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.ConsumeEvents(t.Context(), now, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if project(&snap, p.ID).TitleRevision != p.TitleRevision+2 {
		t.Fatal("second function used stale state")
	}
}

func TestExistingDigestDoesNotTakeWriterLock(t *testing.T) {
	s, c, _, _ := autopilotFixture(t, autopilot.Suggest)
	cfg := s.configuration()
	cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "00:00"}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if digest, err := c.RecordDigest(t.Context(), now, time.UTC); err != nil || digest == nil {
		t.Fatal("initial digest", digest, err)
	}
	other, err := sql.Open("sqlite", filepath.Join(s.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if digest, err := c.RecordDigest(ctx, now, time.UTC); err != nil || digest != nil {
		t.Fatal("duplicate digest attempted a write transaction", digest, err)
	}
}
