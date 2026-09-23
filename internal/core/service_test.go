package core

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

var testContext = context.Background()

func fixture(t *testing.T) (*Service, config.Config) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := config.Default()
	c.Workers = []config.Worker{{ID: "test", Name: "Test worker", Endpoint: "http://127.0.0.1:9999", Capabilities: []string{"coordinate", "implement", "review"}}}
	s := NewService(st, c)
	s.now = func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	return s, c
}
func newProject(t *testing.T, s *Service) Project {
	t.Helper()
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Fictional project", AcceptanceCriteria: "Demonstrated test evidence"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestDecisionResolvesExactlyOnceUnderConcurrency(t *testing.T) {
	s, _ := fixture(t)
	d, err := s.CreateDecision(testContext, DecisionInput{Title: "Audience", Context: "Need focus", Recommendation: "Small pilot", Choices: []string{"Pilot", "Everyone"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := make(chan bool, 2)
	for _, answer := range []string{"Pilot", "Everyone"} {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			_, err := s.ResolveDecision(testContext, d.ID, a)
			success <- err == nil
		}(answer)
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("successful resolutions: %d", n)
	}
}
func TestSourceRefreshPreservesAcceptanceContract(t *testing.T) {
	s, _ := fixture(t)
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Issue", SourceID: "linear:fixture", Description: "Initial source", AcceptanceCriteria: "Explicit acceptance contract"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CreateProject(testContext, ProjectInput{Title: "Updated title", SourceID: p.SourceID, Description: "Updated source", AcceptanceCriteria: "Generic discovery placeholder"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AcceptanceCriteria != p.AcceptanceCriteria || got.Title != "Updated title" {
		t.Fatal(got)
	}
}
func TestPendingOperationInspectionIsVisibleAndDoesNotReplay(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	key := "notify:" + p.ID + ":fixture"
	s.ClaimEvent(testContext, key)
	v, _ := s.Snapshot(testContext)
	if len(v.PendingOperations) != 1 || v.PendingOperations[0].ProjectID != p.ID || v.PendingOperations[0].Summary == "" {
		t.Fatal(v.PendingOperations)
	}
	if err := s.AcknowledgeEvent(testContext, key, ""); err == nil {
		t.Fatal("empty inspection accepted")
	}
	if err := s.AcknowledgeEvent(testContext, key, "Verified the notification was delivered"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.ClaimEvent(testContext, key)
	if fresh {
		t.Fatal("acknowledgement replayed operation")
	}
}
