package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/lib-agent-harness/session"
)

func visibleAutopilotStatus(t *testing.T, a *App) *core.Integration {
	t.Helper()
	snap, err := a.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out *core.Integration
	for _, integration := range snap.Integrations {
		if strings.HasPrefix(integration.ID, "autopilot") {
			if integration.ID != "autopilot" || out != nil {
				t.Fatalf("duplicate subsystem integration: %+v", snap.Integrations)
			}
			copy := integration
			out = &copy
		}
	}
	return out
}

func TestAutopilotErrorsAreVisibleAndClearIndependently(t *testing.T) {
	a := testApp(t)
	if visibleAutopilotStatus(t, a) != nil {
		t.Fatal("idle autopilot advertised readiness")
	}
	for _, id := range []string{"autopilot-events", "autopilot-notifications", "autopilot-digests", "autopilot-summary", "autopilot-acknowledgement"} {
		a.autopilotStatus(id, errors.New("synthetic"), id)
		status := visibleAutopilotStatus(t, a)
		if status == nil || status.Status != "error" || !strings.Contains(status.Detail, id) {
			t.Fatal("error missing from owner snapshot", status)
		}
		a.autopilotStatus("autopilot-events", nil, "")
		if id != "autopilot-events" && visibleAutopilotStatus(t, a) == nil {
			t.Fatal("unrelated success hid error")
		}
		a.autopilotStatus(id, nil, "")
		if visibleAutopilotStatus(t, a) != nil {
			t.Fatal("error did not clear after recovery")
		}
	}
}

func seedAutopilotSuggestion(t *testing.T, a *App) core.AutopilotAction {
	t.Helper()
	p, err := a.Core.CreateProject(t.Context(), core.ProjectInput{Title: "Synthetic", Brief: core.BriefInput{Goal: "Test", Criteria: []string{"Test"}}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := a.Autopilot.Register("authorised-research", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(core.RenameProjectArgs{Title: "Changed"})
	action, err := f.Submit(t.Context(), "synthetic-return", "test summary", core.ConcreteAction{Kind: "rename-project", ProjectID: p.ID, TargetVersion: p.TitleRevision, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	return action
}

type afterPresentationChat struct {
	*fakeChat
	after func()
}

func (f *afterPresentationChat) Turn(ctx context.Context, text string, observe func(session.Event)) (session.Result, error) {
	result, err := f.fakeChat.Turn(ctx, text, observe)
	f.after()
	return result, err
}

func TestAutopilotCancelledOrUnsavedPresentationStaysUnseen(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "save failed"}[failSave], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.db")
			store, err := core.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cfg := config.Default()
			a := New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), Options{})
			own := seated(&a.cfg)
			own.Model.Engine, own.Model.Model = "claude", "opus"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			a.sessions.open = func(_ context.Context, spec chatSpec, _ *session.Ref) (chatModel, session.Opened, error) {
				return &afterPresentationChat{fakeChat: &fakeChat{spec: spec, id: "synthetic"}, after: func() {
					if failSave {
						store.Close()
					} else {
						cancel()
					}
				}}, session.Opened{}, nil
			}
			seedAutopilotSuggestion(t, a)
			if _, err := a.Core.EnqueueChat(ctx, chatID(), "What changed?"); err != nil {
				t.Fatal(err)
			}
			turn, err := a.Core.StartNextChat(ctx, "claude")
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := a.runChatTurn(ctx, turn)
			if failSave && runErr == nil {
				t.Fatal("save failure hidden")
			}
			reopened, err := core.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			c := core.NewAutopilotCoordinator(core.NewService(reopened, cfg), nil)
			seen, err := c.Progress(t.Context(), "owner_seen")
			if err != nil || seen != 0 {
				t.Fatal("unsuccessful presentation acknowledged")
			}
		})
	}
}

func TestAutopilotReturnAcknowledgesSuccessfulSavedTurn(t *testing.T) {
	a, o := sessionApp(t)
	seedAutopilotSuggestion(t, a)
	runTurn(t, a, "What changed?")
	if !strings.Contains(o.chats[0].sent[0], "Autopilot summary:") {
		t.Fatal("return summary not presented")
	}
	seen, err := a.Autopilot.Progress(t.Context(), "owner_seen")
	if err != nil || seen != 1 {
		t.Fatalf("successful saved presentation not acknowledged: %d %v", seen, err)
	}
}

func TestAutopilotFailedReturnKeepsProgress(t *testing.T) {
	a, o := sessionApp(t)
	runTurn(t, a, "Hello")
	seedAutopilotSuggestion(t, a)
	o.chats[0].err = errors.New("synthetic model failure")
	if _, err := a.Core.EnqueueChat(t.Context(), chatID(), "What changed?"); err != nil {
		t.Fatal(err)
	}
	turn, err := a.Core.StartNextChat(t.Context(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.runChatTurn(t.Context(), turn); err == nil {
		t.Fatal("turn unexpectedly succeeded")
	}
	seen, err := a.Autopilot.Progress(t.Context(), "owner_seen")
	if err != nil || seen != 0 {
		t.Fatal("failed presentation acknowledged")
	}
}

func TestAutopilotNotificationsBatchAndFailureRetry(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			a := testApp(t)
			action := seedAutopilotSuggestion(t, a)
			a.Autopilot.OwnerAction(t.Context(), action.ID, 1, "cancel", nil)
			calls := 0
			send := func(context.Context, string) error {
				calls++
				if fail {
					return errors.New("synthetic send failure")
				}
				return nil
			}
			now := time.Now()
			a.notifyAutopilotActionsAt(t.Context(), now, send)
			a.notifyAutopilotActionsAt(t.Context(), now.Add(time.Second), send)
			seen, err := a.Autopilot.Progress(t.Context(), "notified")
			if err != nil {
				t.Fatal(err)
			}
			if fail && seen != 0 {
				t.Fatal("send failure advanced progress")
			}
			if !fail && seen != 2 {
				t.Fatal("send success did not advance progress")
			}
			wantCalls := 1

			if calls != wantCalls {
				t.Fatalf("calls=%d want=%d", calls, wantCalls)
			}
			// An independently constructed App uses the same durable claims/cursor.
			restarted := New(a.Core, a.Config(), a.configPath, Options{})
			if fail {
				fail = false
				restarted.notifyAutopilotActionsAt(t.Context(), now.Add(3*time.Minute), send)
				wantCalls++
				seen, err = restarted.Autopilot.Progress(t.Context(), "notified")
				if err != nil || seen != 2 {
					t.Fatal("covering retry did not advance progress", seen, err)
				}
			}
			restarted.notifyAutopilot(t.Context(), send)
			if calls != wantCalls {
				t.Fatal("restart repeated successful notification")
			}
		})
	}
}

func TestAutopilotNotificationRecoversCompletedRangeBeforeNewEntries(t *testing.T) {
	a := testApp(t)
	old := seedAutopilotSuggestion(t, a)
	a.notifyAutopilotActions(t.Context(), func(context.Context, string) error { return nil })
	// A completed receipt and its cursor survive restart before newer entries.
	f, err := a.Autopilot.Register("ci-failures", "v1", []string{"rename-project"}, func(core.Snapshot, core.ConcreteAction) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Submit(t.Context(), "after-crash", "new proposal", old.Action); err != nil {
		t.Fatal(err)
	}
	restarted := New(a.Core, a.Config(), a.configPath, Options{})
	calls := 0
	send := func(_ context.Context, message string) error {
		calls++
		if strings.Contains(message, "test summary") || !strings.Contains(message, "new proposal") {
			t.Fatalf("completed range repeated or new entry lost: %s", message)
		}
		return nil
	}
	restarted.notifyAutopilot(t.Context(), send)
	restarted.notifyAutopilot(t.Context(), send)
	seen, err := restarted.Autopilot.Progress(t.Context(), "notified")
	if err != nil || seen != 2 || calls != 1 {
		t.Fatalf("recovery: cursor=%d calls=%d error=%v", seen, calls, err)
	}
}

func TestAutopilotSummaryReadFailureDoesNotBlockChat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	a := New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), Options{})
	own := seated(&a.cfg)
	own.Model.Engine, own.Model.Model = "claude", "opus"
	o := &openings{}
	a.sessions.open = o.open
	seedAutopilotSuggestion(t, a)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var original string
	if err := db.QueryRow("SELECT payload FROM autopilot_audit").Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE autopilot_audit SET payload='malformed'"); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "Hello despite the broken summary")
	if status := visibleAutopilotStatus(t, a); status == nil || !strings.Contains(status.Detail, "Unable to read return summary") {
		t.Fatal("summary failure invisible to owner", status)
	}
	if len(o.chats) != 1 || !strings.Contains(o.chats[0].sent[0], "Hello despite") || strings.Contains(o.chats[0].sent[0], "Autopilot summary:") {
		t.Fatal("summary failure blocked or prefixed turn")
	}
	seen, err := a.Autopilot.Progress(t.Context(), "owner_seen")
	if err != nil || seen != 0 {
		t.Fatal("failed summary acknowledged", seen, err)
	}
	if _, err := db.Exec("UPDATE autopilot_audit SET payload=?", original); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "Recovered summary")
	if visibleAutopilotStatus(t, a) != nil {
		t.Fatal("summary status never recovered")
	}
}

func TestAutopilotDigestDeliveryDoesNotBackfill(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "00:00"}
	a.cfg = cfg
	a.Core.UpdateConfig(cfg)
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 23, 30, 0, 0, loc)
	for _, day := range []time.Time{now.AddDate(0, 0, -3), now.AddDate(0, 0, -1), now} {
		if _, err := a.Autopilot.RecordDigest(t.Context(), day, loc); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	send := func(_ context.Context, msg string) error {
		calls++
		if !strings.Contains(msg, "Daily digest "+now.Format("2006-01-02")) {
			t.Fatal("old digest sent", msg)
		}
		return nil
	}
	a.notifyAutopilotDigestsAt(t.Context(), now, loc, send)
	restarted := New(a.Core, a.Config(), a.configPath, Options{})
	restarted.notifyAutopilotDigestsAt(t.Context(), now, loc, send)
	restarted.notifyAutopilotDigestsAt(t.Context(), now.Add(time.Hour), loc, send)
	if calls != 1 {
		t.Fatal("digest duplicate/backfill", calls)
	}
}

func TestAutopilotNotificationOutageIsBoundedAcrossRestart(t *testing.T) {
	a := testApp(t)
	seedAutopilotSuggestion(t, a)
	now := time.Now()
	calls := 0
	fail := func(context.Context, string) error { calls++; return errors.New("offline") }
	// Four hours of supervise ticks, with a restart halfway through the outage.
	for i := 0; i < 960; i++ {
		if i == 480 {
			a = New(a.Core, a.Config(), a.configPath, Options{})
		}
		a.notifyAutopilotActionsAt(t.Context(), now.Add(time.Duration(i)*15*time.Second), fail)
	}
	if calls < 2 || calls > 10 {
		t.Fatalf("retry backoff: %d sends", calls)
	}
	snap, err := a.Core.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for key := range snap.Events {
		if strings.HasPrefix(key, "notify:autopilot:") {
			t.Fatalf("automatic retry leaked claim: %s", key)
		}
	}
	activities := 0
	for _, activity := range snap.Activity {
		if activity.Kind == "autopilot.notification.failed" {
			activities++
		}
	}
	if activities != 1 {
		t.Fatalf("outage activities: %d", activities)
	}
	if seen, err := a.Autopilot.Progress(t.Context(), "notified"); err != nil || seen != 0 {
		t.Fatal("failure advanced cursor", seen, err)
	}
	a.notifyAutopilotActionsAt(t.Context(), now.Add(5*time.Hour), func(context.Context, string) error { calls++; return nil })
	if seen, err := a.Autopilot.Progress(t.Context(), "notified"); err != nil || seen != 1 {
		t.Fatal("recovery failed", seen, err)
	}
	if visibleAutopilotStatus(t, a) != nil {
		t.Fatal("notification status did not recover")
	}
	previous := calls
	a = New(a.Core, a.Config(), a.configPath, Options{})
	a.notifyAutopilotActionsAt(t.Context(), now.Add(6*time.Hour), fail)
	if calls != previous {
		t.Fatal("restart repeated successful presentation")
	}
}

func TestAutopilotAcknowledgementStatusRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	a := New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), Options{})
	own := seated(&a.cfg)
	own.Model.Engine, own.Model.Model = "claude", "opus"
	o := &openings{}
	a.sessions.open = o.open
	seedAutopilotSuggestion(t, a)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_ack BEFORE INSERT ON autopilot_progress BEGIN SELECT RAISE(ABORT,'ack unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "Show the summary")
	if visibleAutopilotStatus(t, a) == nil {
		t.Fatal("ack failure not visible")
	}
	if seen, err := a.Autopilot.Progress(t.Context(), "owner_seen"); err != nil || seen != 0 {
		t.Fatal("failed ack advanced cursor", seen, err)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_ack`); err != nil {
		t.Fatal(err)
	}
	runTurn(t, a, "Show it again")
	if visibleAutopilotStatus(t, a) != nil {
		t.Fatal("ack status never recovered")
	}
	if seen, err := a.Autopilot.Progress(t.Context(), "owner_seen"); err != nil || seen != 1 {
		t.Fatal("ack recovery failed", seen, err)
	}
}

func TestAutopilotDispatcherStatusRecoversOnNudge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	a := New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), Options{})
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER reject_heartbeat BEFORE INSERT ON autopilot_events BEGIN SELECT RAISE(ABORT,'event unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); a.runAutopilotEvents(lifecycle.Now(ctx)) }()
	defer func() { cancel(); <-done }()
	waitStatus := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			got := "absent"
			if status := visibleAutopilotStatus(t, a); status != nil {
				got = status.Status
			}
			if got == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("dispatcher never reached status %s", want)
	}
	waitStatus("error")
	if _, err := db.Exec(`DROP TRIGGER reject_heartbeat`); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.RecordCIEvent(t.Context(), core.CIEvent{Provider: "synthetic", Repo: "example/repo", Ref: "main", Commit: "abc", Check: "test", State: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	waitStatus("absent")
}

func TestAutopilotDigestFailureBacksOffAndRecovers(t *testing.T) {
	a := testApp(t)
	cfg := a.Config()
	cfg.Autopilot.DailyDigest = autopilot.DailyDigest{Enabled: true, At: "00:00"}
	a.cfg = cfg
	a.Core.UpdateConfig(cfg)
	now := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	if _, err := a.Autopilot.RecordDigest(t.Context(), now, time.UTC); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := func(context.Context, string) error { calls++; return errors.New("offline") }
	for i := 0; i < 120; i++ {
		a.notifyAutopilotDigestsAt(t.Context(), now.Add(time.Duration(i)*time.Second), time.UTC, fail)
	}
	if calls != 1 {
		t.Fatal("digest retry flood", calls)
	}
	if status := visibleAutopilotStatus(t, a); status == nil || !strings.Contains(status.Detail, "Digest notification failed") {
		t.Fatal("digest failure invisible to owner", status)
	}
	a = New(a.Core, a.Config(), a.configPath, Options{})
	a.notifyAutopilotDigestsAt(t.Context(), now.Add(2*time.Minute), time.UTC, func(context.Context, string) error { calls++; return nil })
	a.notifyAutopilotDigestsAt(t.Context(), now.Add(3*time.Minute), time.UTC, fail)
	if calls != 2 {
		t.Fatal("digest recovery/receipt", calls)
	}
	if visibleAutopilotStatus(t, a) != nil {
		t.Fatal("digest status never recovered")
	}
}
