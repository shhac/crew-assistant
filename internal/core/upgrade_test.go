package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func updateFixture(t *testing.T, mode string) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := config.Default()
	cfg.Upgrade.Mode = mode
	return NewService(store, cfg), path
}
func updateResult(version string) upgrade.Result {
	return upgrade.Result{Available: version, Notes: "Release notes", URL: "https://fixture.invalid/release", CheckedAt: time.Now().UTC()}
}
func recordUpdate(t *testing.T, s *Service, version string) Snapshot {
	t.Helper()
	if err := s.RecordUpdateCheck(context.Background(), "v1.0.0", updateResult(version)); err != nil {
		t.Fatal(err)
	}
	v, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUpdateAskAtomicIdempotentAcrossRestart(t *testing.T) {
	s, path := updateFixture(t, "ask")
	v := recordUpdate(t, s, "v1.1.0")
	if len(v.Decisions) != 1 || v.Decisions[0].ProjectID != "" || v.Decisions[0].Kind != DecisionUpgradeAvailable {
		t.Fatal(v.Decisions)
	}
	for _, word := range []string{"v1.0.0", "v1.1.0", "Release notes", "brew upgrade shhac/tap/crew-assistant", "restart"} {
		if !strings.Contains(v.Decisions[0].Context, word) {
			t.Fatal(word, v.Decisions[0])
		}
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.RecordUpdateCheck(context.Background(), "v1.0.0", updateResult("1.1.0")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened := NewService(store, config.Default())
	v = recordUpdate(t, reopened, "v1.1.0")
	if len(v.Decisions) != 1 || v.Update.DecisionVersion != "v1.1.0" {
		t.Fatal(v)
	}
}
func TestUpdateOffAndSuperseding(t *testing.T) {
	s, _ := updateFixture(t, "off")
	v := recordUpdate(t, s, "v1.1.0")
	if len(v.Decisions) != 0 || v.Update.Available != "v1.1.0" {
		t.Fatal(v)
	}
	cfg := s.configuration()
	cfg.Upgrade.Mode = "ask"
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	v = recordUpdate(t, s, "v1.1.0")
	old := v.Decisions[0].ID
	cfg.Upgrade.Mode = "off"
	s.UpdateConfig(cfg)
	v = recordUpdate(t, s, "v1.1.0")
	if v.Decisions[0].Status != DecisionOpen {
		t.Fatal(v.Decisions)
	}
	v = recordUpdate(t, s, "v1.2.0")
	if len(v.Decisions) != 1 || v.Decisions[0].Status != DecisionResolved || v.Decisions[0].Disposition != DispositionSuperseded {
		t.Fatal(v.Decisions)
	}
	if _, err := s.ChooseDecision(context.Background(), old, ChoiceUpgradeByHand, FromOwner); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	cfg.Upgrade.Mode = "ask"
	s.UpdateConfig(cfg)
	v = recordUpdate(t, s, "v1.2.0")
	if len(v.Decisions) != 2 || v.Decisions[1].Status != DecisionOpen {
		t.Fatal(v.Decisions)
	}
	newer := recordUpdate(t, s, "v1.3.0")
	if len(newer.Decisions) != 3 || newer.Decisions[1].Status != DecisionResolved || newer.Decisions[1].Disposition != DispositionSuperseded || newer.Decisions[2].Status != DecisionOpen {
		t.Fatal(newer.Decisions)
	}
	if _, err := s.AnswerDecision(context.Background(), v.Decisions[1].ID, "yes", FromOwner); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	v = recordUpdate(t, s, "v1.2.0")
	if v.Update.Available != "v1.3.0" || len(v.Decisions) != 3 {
		t.Fatal(v)
	}
}
func TestUpdateSkipDismissCustomAndHand(t *testing.T) {
	for _, answer := range []string{"skip", "dismiss", "custom", "hand"} {
		t.Run(answer, func(t *testing.T) {
			s, _ := updateFixture(t, "ask")
			v := recordUpdate(t, s, "v1.1.0")
			id := v.Decisions[0].ID
			var err error
			switch answer {
			case "skip":
				_, err = s.ChooseDecision(context.Background(), id, "Skip v1.1.0", FromOwner)
			case "dismiss":
				_, err = s.DismissDecision(context.Background(), id, "Later")
			case "custom":
				_, err = s.AnswerDecision(context.Background(), id, "Later", FromOwner)
			case "hand":
				_, err = s.ChooseDecision(context.Background(), id, ChoiceUpgradeByHand, FromOwner)
			}
			if err != nil {
				t.Fatal(err)
			}
			v = recordUpdate(t, s, "v1.1.0")
			if len(v.Decisions) != 1 || (answer != "hand" && v.Update.Skipped != "v1.1.0") || (answer == "hand" && v.Update.Skipped != "") {
				t.Fatal(v.Update, v.Decisions)
			}
			v = recordUpdate(t, s, "v1.2.0")
			if len(v.Decisions) != 2 || v.Decisions[1].Status != DecisionOpen {
				t.Fatal(v.Decisions)
			}
		})
	}
}
func TestUpdateFailureRetainsAndRunningClears(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	before := recordUpdate(t, s, "v1.1.0")
	fail := upgrade.Result{Error: "HTTP 429", CheckedAt: time.Now().UTC()}
	if err := s.RecordUpdateCheck(context.Background(), "v1.0.0", fail); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Snapshot(context.Background())
	if v.Update.Available != before.Update.Available || v.Update.Notes != before.Update.Notes || v.Update.Error != fail.Error || v.Decisions[0].Status != DecisionOpen {
		t.Fatal(v)
	}
	for _, running := range []string{"v1.1.0", "v2.0.0"} {
		if err := s.ReconcileRunningUpdate(context.Background(), running); err != nil {
			t.Fatal(err)
		}
		v, _ = s.Snapshot(context.Background())
		if v.Update.Available != "" || v.Decisions[0].Status == DecisionOpen {
			t.Fatal(v)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RecordUpdateCheck(ctx, "v1.0.0", updateResult("v2.0.0")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	v, _ = s.Snapshot(context.Background())
	if v.Update.Available != "" {
		t.Fatal("cancelled check persisted")
	}
}

func TestReleaseRecordedObserverRunsOnlyAfterCommit(t *testing.T) {
	s, _ := fixture(t)
	p := releaseProject(t, s, ApprovePM)
	calls := 0
	s.OnReleaseRecorded(func(repo, version string) {
		calls++
		v, err := s.Snapshot(testContext)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := findProjectByID(v, p.ID)
		if repo != "fixture/application" || version != "v1.1.0" || got.Release != nil || len(got.Releases) != 1 {
			t.Fatal(repo, version, got)
		}
		found := false
		for _, e := range v.Activity {
			if e.Kind == "release.recorded" && e.ProjectID == p.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("observer preceded release.recorded")
		}
	})
	if err := s.FinishRelease(testContext, p.ID, "fixture/application", ""); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("failed transaction notified")
	}
	s.store.update(testContext, func(v *Snapshot) error {
		project(v, p.ID).Release = &ReleaseRun{ReleaseProposal: ReleaseProposal{Version: "v1.1.0"}, State: "publishing"}
		return nil
	})
	if err := s.FinishRelease(testContext, p.ID, "fixture/application", ""); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestRunningUpdateCompletesDecisionWithoutAnOwnerAnswer(t *testing.T) {
	for _, running := range []string{"v1.1.0", "v2.0.0"} {
		t.Run(running, func(t *testing.T) {
			s, _ := updateFixture(t, "ask")
			v := recordUpdate(t, s, "v1.1.0")
			id := v.Decisions[0].ID
			if err := s.ReconcileRunningUpdate(context.Background(), running); err != nil {
				t.Fatal(err)
			}
			v, err := s.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			d := v.Decisions[0]
			if v.Update.Available != "" || d.Status != DecisionResolved || d.Disposition != DispositionCompleted || d.Answer != "" || d.ResolvedAt == nil || d.ResolutionReason == "" {
				t.Fatal(v.Update, d)
			}
			found := false
			for _, e := range v.Activity {
				if e.Kind == "decision.resolved" && strings.Contains(e.Summary, "running version") {
					found = true
				}
				if e.Kind == "decision.dismissed" {
					t.Fatal("completed update dismissed", e)
				}
			}
			if !found {
				t.Fatal("completion not recorded")
			}
			if _, err := s.ChooseDecision(context.Background(), id, ChoiceUpgradeByHand, FromOwner); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}
