package core

import (
	"context"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestUpgradeAdmissionClosesConcurrentRoleAndChatClaims(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queueAll(t, s, p, "Queued work")
	if _, err := s.EnqueueChat(testContext, "waiting", "owner reply"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := s.AdmitUpgrade(testContext, func(Snapshot, config.Config) (bool, error) { close(entered); <-release; return true, nil })
		done <- err
	}()
	<-entered
	scheduled := make(chan []Scheduled, 1)
	chat := make(chan error, 1)
	go func() {
		out, err := s.Schedule(testContext, anyone)
		if err != nil {
			t.Errorf("schedule while upgrade admitted: %v", err)
			scheduled <- nil
			return
		}
		scheduled <- out
	}()
	go func() { _, err := s.StartNextChat(testContext, "claude"); chat <- err }()
	select {
	case <-scheduled:
		t.Fatal("role claim bypassed admission lock")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out := <-scheduled; len(out) != 0 {
		t.Fatal(out)
	}
	if err := <-chat; err != ErrNotFound {
		t.Fatal(err)
	}
	// Work admitted before a forced upgrade can still release its claim.
	if err := s.RecordActivity(context.Background(), "", "test", "running work may finish"); err != nil {
		t.Fatal(err)
	}
}
