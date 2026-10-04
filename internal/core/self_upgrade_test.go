package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/upgrade"
)

func TestUpgradeChoiceCallsHookWithoutSkippingAndRejectsClosedDecision(t *testing.T) {
	ctx := context.Background()
	s, _ := updateFixture(t, "ask")
	called := ""
	s.OnUpgradeRequested(func(version string) error { called = version; return nil })
	v := recordUpdate(t, s, "v2.0.0")
	d := v.Decisions[0]
	if d.Choices[0] != UpgradeChoice("v2.0.0") || d.Recommendation != d.Choices[0] {
		t.Fatal(d)
	}
	if _, err := s.ChooseDecision(ctx, d.ID, d.Choices[0], FromOwner); err != nil {
		t.Fatal(err)
	}
	v, err := s.Snapshot(ctx)
	if err != nil || called != "v2.0.0" || v.Update.Skipped != "" {
		t.Fatal(v.Update, called, err)
	}
	if _, err = s.ChooseDecision(ctx, d.ID, d.Choices[0], FromOwner); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	called = ""
	v = recordUpdate(t, s, "v3.0.0")
	d = v.Decisions[len(v.Decisions)-1]
	if _, err = s.AnswerDecision(ctx, d.ID, d.Choices[0], FromOwner); err != nil {
		t.Fatal(err)
	}
	if called != "" {
		t.Fatal("custom answer invoked upgrade")
	}
}

func TestUpgradeFailureOncePerAttemptAndExplicitRetry(t *testing.T) {
	s, _ := updateFixture(t, "automatic")
	ctx := context.Background()
	r := upgrade.Record{From: "v1.0.0", To: "v2.0.0", StartedAt: time.Now(), Pinned: true, Failure: "health check failed", DetachedLog: "/fixture/rollback.log"}
	for range 2 {
		if err := s.RecordUpgradeFailure(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.Snapshot(ctx)
	if err != nil || len(v.Decisions) != 1 || v.Update.Failed != r.To {
		t.Fatal(v, err)
	}
	d := v.Decisions[0]
	for _, text := range []string{"Rollback is in force", "upgrade clear-rollback", r.Failure, r.DetachedLog} {
		if !strings.Contains(d.Context, text) {
			t.Fatal(d)
		}
	}
	if d.Kind != DecisionUpgradeFailed || d.Choices[0] != "Stay on v1.0.0" || d.Choices[1] != RetryUpgradeChoice(r.To) {
		t.Fatal(d)
	}
	called := ""
	s.OnUpgradeRequested(func(version string) error { called = version; return nil })
	if _, err = s.ChooseDecision(ctx, d.ID, d.Choices[1], FromOwner); err != nil || called != r.To {
		t.Fatal(called, err)
	}
}

func TestExistingUpgradeNoticeRefreshesInstallCapability(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	v := recordUpdate(t, s, "v2.0.0")
	id := v.Decisions[0].ID
	s.OnUpgradeRequested(func(string) error { return nil })
	v = recordUpdate(t, s, "v2.0.0")
	if len(v.Decisions) != 1 || v.Decisions[0].ID != id || v.Decisions[0].Choices[0] != UpgradeChoice("v2.0.0") {
		t.Fatal(v.Decisions)
	}
	s.OnUpgradeRequested(nil)
	v = recordUpdate(t, s, "v2.0.0")
	if len(v.Decisions) != 1 || v.Decisions[0].Choices[0] != ChoiceUpgradeByHand {
		t.Fatal(v.Decisions)
	}
}

func TestStoreBackupIncludesCommittedWALAndIsPrivate(t *testing.T) {
	s, path := updateFixture(t, "ask")
	ctx := context.Background()
	if err := s.RecordActivity(ctx, "", "fixture", "Committed before backup"); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(path), "snapshot.db")
	if err := s.store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	copy, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	v, err := copy.Snapshot(ctx)
	if err != nil || len(v.Activity) == 0 || v.Activity[len(v.Activity)-1].Summary != "Committed before backup" {
		t.Fatal(v.Activity, err)
	}
	if err = s.store.Backup(ctx, backup); err == nil {
		t.Fatal("overwrote backup")
	}
}

func TestUpgradeHookRunsAfterDecisionCommit(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	ctx := context.Background()
	s.OnUpgradeRequested(func(string) error {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			return err
		}
		if snap.Decisions[0].Status != DecisionResolved {
			t.Fatal("hook ran before commit")
		}
		return nil
	})
	v := recordUpdate(t, s, "v2.0.0")
	if _, err := s.ChooseDecision(ctx, v.Decisions[0].ID, UpgradeChoice("v2.0.0"), FromOwner); err != nil {
		t.Fatal(err)
	}
}
func TestFailureHistoryDoesNotRegressAndHealthyClosesNotices(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	ctx := context.Background()
	for i, target := range []string{"v3.0.0", "v2.0.0"} {
		r := upgrade.Record{From: "v1.0.0", To: target, Pinned: true, Failure: "failed", StartedAt: time.Unix(int64(i), 0)}
		if err := s.RecordUpgradeFailure(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(ctx)
	if snap.Update.Failed != "v3.0.0" {
		t.Fatal(snap.Update)
	}
	if err := s.ReconcileRunningUpdate(ctx, "v3.0.0"); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(ctx)
	for _, d := range snap.Decisions {
		if d.Kind == DecisionUpgradeFailed && d.Status == DecisionOpen {
			t.Fatal("obsolete rollback notice", d)
		}
	}
}

func TestAbandonedCommittedUpgradeCanBeOfferedAgain(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	ctx := context.Background()
	s.OnUpgradeRequested(func(string) error { return nil })
	v := recordUpdate(t, s, "v2.0.0")
	if _, err := s.ChooseDecision(ctx, v.Decisions[0].ID, UpgradeChoice("v2.0.0"), FromOwner); err != nil {
		t.Fatal(err)
	}
	if err := s.ReofferAbandonedUpgrade(ctx, "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	v = recordUpdate(t, s, "v2.0.0")
	if len(v.Decisions) != 2 || v.Decisions[1].Status != DecisionOpen {
		t.Fatal(v.Decisions)
	}
}

func TestCommittedUpgradeIntentReoffersAfterCrash(t *testing.T) {
	for _, retry := range []bool{false, true} {
		s, _ := updateFixture(t, "ask")
		s.OnUpgradeRequested(func(string) error { panic("crash after commit") })
		var d Decision
		if retry {
			if err := s.RecordUpgradeFailure(testContext, upgrade.Record{From: "v1.0.0", To: "v2.0.0", StartedAt: time.Now(), Failure: "failed"}); err != nil {
				t.Fatal(err)
			}
			v, _ := s.Snapshot(testContext)
			d = v.Decisions[0]
		} else {
			v := recordUpdate(t, s, "v2.0.0")
			d = v.Decisions[0]
		}
		choice := UpgradeChoice("v2.0.0")
		if retry {
			choice = RetryUpgradeChoice("v2.0.0")
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Error("hook did not reach crash boundary")
				}
			}()
			_, _ = s.ChooseDecision(testContext, d.ID, choice, FromOwner)
		}()
		v, _ := s.Snapshot(testContext)
		if v.Update.PendingRequest == nil || decision(&v, d.ID).Status != DecisionResolved {
			t.Fatal(v.Update)
		}
		if err := s.ReconcileUpgradeRequests(testContext, "v1.0.0", nil); err != nil {
			t.Fatal(err)
		}
		v, _ = s.Snapshot(testContext)
		if v.Update.PendingRequest != nil || decision(&v, d.ID).Status != DecisionOpen {
			t.Fatal(v.Update, v.Decisions)
		}
	}
}

func TestFailingHookCannotResurrectSupersededNotice(t *testing.T) {
	s, _ := updateFixture(t, "ask")
	entered, release := make(chan struct{}), make(chan struct{})
	s.OnUpgradeRequested(func(string) error { close(entered); <-release; return errors.New("failed drain") })
	v := recordUpdate(t, s, "v2.0.0")
	old := v.Decisions[0]
	done := make(chan error, 1)
	go func() {
		_, err := s.ChooseDecision(testContext, old.ID, UpgradeChoice("v2.0.0"), FromOwner)
		done <- err
	}()
	<-entered
	recordUpdate(t, s, "v3.0.0")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("hook failure hidden")
	}
	v, _ = s.Snapshot(testContext)
	if decision(&v, old.ID).Status == DecisionOpen || v.Update.DecisionVersion != "v3.0.0" {
		t.Fatal(v.Update, v.Decisions)
	}
	if _, err := s.ChooseDecision(testContext, old.ID, "Skip v2.0.0", FromOwner); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	v, _ = s.Snapshot(testContext)
	if v.Update.Skipped != "" {
		t.Fatal(v.Update)
	}
}
