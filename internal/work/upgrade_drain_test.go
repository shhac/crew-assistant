package work

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

type upgradeBlockedRunner struct{ *gatedRunner }

func (g upgradeBlockedRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if spec.Observer != nil {
		spec.Observer.Started()
		defer spec.Observer.Ended()
		spec.Observer = nil
	}
	return g.gatedRunner.Run(ctx, spec)
}

func TestUpgradeDrainWaitsForRoleAndStartsNoOther(t *testing.T) {
	a, g := gatedLoop(t)
	a.runner = upgradeBlockedRunner{g}
	stop, cancel := lifecycle.Watch(context.Background(), make(chan os.Signal), func(string) {})
	defer cancel()
	done := runLoop(a, stop)
	waitFor(t, g.entered, "no role started")
	turns := a.Turns()
	if len(turns) != 1 {
		t.Fatal(turns)
	}
	path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
	installed := false
	e := &upgrade.Engine{Path: path, Drain: func() bool { return stop.Drain("upgrade") }, Backup: func(context.Context, upgrade.Record) error { return nil }, Install: func(context.Context, upgrade.Record) (string, error) { installed = true; return "v2.0.0", nil }, Handover: func(context.Context, upgrade.Record) error { return nil }, Exec: func(string, []string) error { return nil }}
	r := upgrade.Record{From: "v1.0.0", To: "v2.0.0", WaitingOn: []upgrade.WaitingOn{{Kind: turns[0].Role, Ref: turns[0].TaskID, StartedAt: turns[0].StartedAt}}}
	if err := e.Request(r); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		if err := stop.Await(done); err != nil {
			finished <- err
			return
		}
		finished <- e.FinishDrain(context.Background(), stop.Reason())
	}()
	select {
	case err := <-finished:
		t.Fatal("installed before running role finished", err)
	case <-time.After(30 * time.Millisecond):
	}
	got, _ := upgrade.ReadRecord(path)
	if got.Step != upgrade.Draining || len(got.WaitingOn) != 1 {
		t.Fatal(got)
	}
	close(g.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !installed || g.turns.Load() != 1 {
		t.Fatal(installed, g.turns.Load())
	}
}

func TestPMDecisionAndReplyReportLiveTurns(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(map[bool]string{false: "decision", true: "reply"}[reply], func(t *testing.T) {
			scripted := &scriptedRunner{}
			lp, p, _, _ := pmTeam(t, scripted)
			entered, release := make(chan struct{}), make(chan struct{})
			lp.runner = pmChatRunner{run: func(ctx context.Context, s roles.Spec) (roles.Result, error) {
				if s.Observer == nil {
					t.Error("PM turn has no observer")
					return roles.Result{}, nil
				}
				s.Observer.Started()
				defer s.Observer.Ended()
				close(entered)
				<-release
				if reply {
					return roles.Result{Text: "The first task comes next."}, nil
				}
				return scripted.Run(ctx, s)
			}}
			if reply {
				if _, err := lp.SendPMChat(context.Background(), p.ID, "upgrade-pm", "What next?"); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := lp.loopStep(context.Background(), false); done <- err }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("PM did not start")
			}
			turns := lp.Turns()
			if len(turns) != 1 || turns[0].Role != core.RolePM || turns[0].ProjectID != p.ID {
				t.Fatal(turns)
			}

			if reply {
				snapshot, err := lp.Core.Snapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if turns[0].MessageID == "" {
					t.Fatal("blocked PM reply lacks operation identity", turns)
				}
				found := false
				for _, message := range snapshot.PMChats {
					if message.ID == turns[0].MessageID && message.Status == "working" {
						found = true
					}
				}
				if !found {
					t.Fatal("observer does not identify working PM chat", turns, snapshot.PMChats)
				}
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if turns := lp.Turns(); len(turns) != 0 {
				t.Fatal(turns)
			}
		})
	}
}
