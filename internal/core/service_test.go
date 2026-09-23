package core

import (
	"context"
	"github.com/shhac/crew-assistant/internal/config"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
func delegate(t *testing.T, s *Service, p Project, parent string) Agent {
	t.Helper()
	a, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ParentID: parent, ProfileID: "test", Role: "manager", Task: "Coordinate isolated work", AcceptanceCriteria: "Evidence reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func start(t *testing.T, s *Service, a Agent) {
	t.Helper()
	if _, err := s.BeginDispatch(testContext, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDispatched(testContext, a.ID, "external-"+a.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRestartPreservesStateAndDispatchIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.Workers = []config.Worker{{ID: "test", Endpoint: "http://localhost:9000", Capabilities: []string{"coordinate"}}}
	s := NewService(st, c)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	if _, err = s.BeginDispatch(testContext, a.ID); err != nil {
		t.Fatal(err)
	}
	s.Remember(testContext, "preference", "Concise recommendations")
	s.AddMessage(testContext, "user", "Please coordinate this")
	if err = s.ReserveModelCall(testContext, 1); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimEvent(testContext, "slack-event"); err != nil || !claimed {
		t.Fatal(claimed, err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, c)
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 1 || len(snap.Messages) != 1 || len(snap.Memories) != 1 || snap.Agents[0].Status != "dispatching" || snap.Agents[0].DispatchKey != a.DispatchKey {
		t.Fatalf("lost durable state: %+v", snap)
	}
	if _, err = s.BeginDispatch(testContext, a.ID); err == nil {
		t.Fatal("duplicate launch allowed")
	}
	if err = s.ReserveModelCall(testContext, 1); err == nil {
		t.Fatal("quota lost on restart")
	}
	if claimed, err := s.ClaimEvent(testContext, "slack-event"); err != nil || claimed {
		t.Fatal("event duplicated", claimed, err)
	}
	pending, err := s.PendingEvents(testContext)
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
}
func TestCapacityAtDispatchWaitingManagerReleasesSlot(t *testing.T) {
	s, c := fixture(t)
	c.Limits.MaxAgents = 1
	s.UpdateConfig(c)
	p := newProject(t, s)
	manager := delegate(t, s, p, "")
	start(t, s, manager)
	child := delegate(t, s, p, manager.ID)
	if _, err := s.BeginDispatch(testContext, child.ID); err == nil {
		t.Fatal("overcommitted execution capacity")
	}
	if _, err := s.UpdateAgent(testContext, manager.ID, AgentUpdate{Status: "waiting", Summary: "Waiting on child"}); err != nil {
		t.Fatal(err)
	}
	start(t, s, child)
}
func TestDelegationScopeDepthAndCycles(t *testing.T) {
	s, c := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	c.Limits.MaxDepth = 1
	s.UpdateConfig(c)
	if _, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ParentID: a.ID, ProfileID: "test", Role: "worker", Task: "Task", AcceptanceCriteria: "Evidence"}); err == nil {
		t.Fatal("depth limit bypass")
	}
	if _, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ParentID: "nonexistent", ProfileID: "test", Role: "worker", Task: "Task", AcceptanceCriteria: "Evidence"}); err == nil {
		t.Fatal("unknown parent permitted")
	}
	p2 := newProject(t, s)
	if _, err := s.Delegate(testContext, DelegateInput{ProjectID: p2.ID, ParentID: a.ID, ProfileID: "test", Role: "worker", Task: "Task", AcceptanceCriteria: "Evidence"}); err == nil {
		t.Fatal("cross project parent permitted")
	}
	if _, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ProfileID: "test", Role: "worker", Task: "Task", AcceptanceCriteria: "Evidence", Capabilities: []string{"deploy"}}); err == nil {
		t.Fatal("prohibited authority permitted")
	}
	// Parent links are immutable and IDs generated only on creation: a new node
	// cannot be its own existing parent or introduce an ancestor cycle.
	c.Limits.MaxDepth = 3
	s.UpdateConfig(c)
	limited, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ProfileID: "test", Role: "manager", Task: "Coordinate", AcceptanceCriteria: "Evidence", Capabilities: []string{"coordinate"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ParentID: limited.ID, ProfileID: "test", Role: "worker", Task: "Implement", AcceptanceCriteria: "Evidence", Capabilities: []string{"implement"}}); err == nil {
		t.Fatal("child widened inherited authority")
	}
}
func TestCompletionRequiresEvidenceAndSettledChildren(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	if _, err := s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "completed", Summary: "Done"}); err == nil {
		t.Fatal("completion without evidence")
	}
	child := delegate(t, s, p, a.ID)
	if _, err := s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "completed", Summary: "Done", Evidence: []string{"Checks passed"}}); err == nil {
		t.Fatal("completion despite unfinished child")
	}
	start(t, s, child)
	if _, err := s.UpdateAgent(testContext, child.ID, AgentUpdate{Status: "completed", Summary: "Done", Evidence: []string{"Tests passed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "completed", Summary: "Verified", Evidence: []string{"Review passed"}}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Projects[0].Status == "completed" {
		t.Fatal("worker auto-closed project")
	}
	if err := s.CompleteProject(testContext, p.ID, nil); err == nil {
		t.Fatal("project completion without evidence")
	}
	if err := s.CompleteProject(testContext, p.ID, []string{"Acceptance verified"}); err != nil {
		t.Fatal(err)
	}
}
func TestMissedCheckInDoesNotLaunchDuplicate(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	s.now = func() time.Time { return time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC) }
	if err := s.CheckIn(testContext); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Agents[0].Status != "reconciling" {
		t.Fatal(snap.Agents[0].Status)
	}
	if _, err := s.PrepareResume(testContext, a.ID); err == nil {
		t.Fatal("silence authorized resume")
	}
	events := len(snap.Activity)
	s.CheckIn(testContext)
	snap, _ = s.Snapshot(testContext)
	if len(snap.Activity) != events {
		t.Fatal("repeated watchdog event")
	}
}
func TestResumeRetainsSessionAndBoundsRetries(t *testing.T) {
	s, c := fixture(t)
	c.Limits.MaxRecoveries = 1
	s.UpdateConfig(c)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "interrupted", Summary: "Process stopped"})
	resumed, err := s.PrepareResume(testContext, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExternalID != "external-"+a.ID || resumed.DispatchKey != a.DispatchKey || resumed.ResumeKey == "" || resumed.Status != "resuming" {
		t.Fatal(resumed)
	}
	if _, err = s.PrepareResume(testContext, a.ID); err == nil {
		t.Fatal("repeated resume while uncertain")
	}
	s.MarkDispatched(testContext, a.ID, resumed.ExternalID)
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "interrupted", Summary: "Stopped again"})
	if _, err = s.PrepareResume(testContext, a.ID); err == nil {
		t.Fatal("unbounded recovery")
	}
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
func TestDemoIsExplicitAndPaused(t *testing.T) {
	s, _ := fixture(t)
	v, _ := s.Snapshot(testContext)
	if len(v.Projects) != 0 {
		t.Fatal("automatic fixtures")
	}
	if err := s.SeedDemo(testContext); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Snapshot(testContext)
	if !v.Paused || !strings.Contains(v.Activity[0].Summary, "Fictional") {
		t.Fatal("demo not identified and paused")
	}
	if err := s.SeedDemo(testContext); err == nil {
		t.Fatal("demo overwrote data")
	}
}

func TestBrokerTimestampIsNotPollingTime(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	reported := s.now().Add(-20 * time.Minute)
	if _, err := s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "running", Summary: "Implementing", UpdatedAt: reported}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return reported.Add(40 * time.Minute) }
	if _, err := s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "running", Summary: "Implementing", UpdatedAt: reported}); err != nil {
		t.Fatal(err)
	}
	s.CheckIn(testContext)
	v, _ := s.Snapshot(testContext)
	if v.Agents[0].Status != "reconciling" || !v.Agents[0].LastUpdate.Equal(reported) {
		t.Fatal("polling refreshed worker liveness", v.Agents[0])
	}
}

func TestRevokedProfileCannotResumeOrDispatch(t *testing.T) {
	s, c := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "interrupted", Summary: "Stopped"})
	queued := delegate(t, s, p, "")
	c.Workers[0].Capabilities = []string{"coordinate"}
	s.UpdateConfig(c)
	if _, err := s.PrepareResume(testContext, a.ID); err == nil {
		t.Fatal("resumed revoked authority")
	}
	if _, err := s.BeginDispatch(testContext, queued.ID); err == nil {
		t.Fatal("dispatched revoked authority")
	}
}
func TestWaitingWorkerRetainsCapacityAndManagerWakeReservesIt(t *testing.T) {
	s, c := fixture(t)
	c.Limits.MaxAgents = 1
	s.UpdateConfig(c)
	p := newProject(t, s)
	manager := delegate(t, s, p, "")
	start(t, s, manager)
	s.UpdateAgent(testContext, manager.ID, AgentUpdate{Status: "waiting", Summary: "Waiting for child"})
	child, err := s.Delegate(testContext, DelegateInput{ProjectID: p.ID, ParentID: manager.ID, ProfileID: "test", Role: "worker", Task: "Work", AcceptanceCriteria: "Evidence"})
	if err != nil {
		t.Fatal(err)
	}
	start(t, s, child)
	s.UpdateAgent(testContext, child.ID, AgentUpdate{Status: "waiting", Summary: "Broker queue"})
	if err = s.BeginInstruction(testContext, manager.ID); err == nil {
		t.Fatal("manager wake exceeded capacity")
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
func TestUnexecutedProjectCannotClaimCompletion(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	if err := s.CompleteProject(testContext, p.ID, []string{"The model says it is done"}); err == nil {
		t.Fatal("completion fabricated without commissioned work")
	}
}
func TestHeartbeatsDoNotCountAsSubstantiveProgress(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	first := s.now().UTC()
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "running", Summary: "Working", UpdatedAt: first})
	s.now = func() time.Time { return first.Add(2 * time.Hour) }
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "running", Summary: "Working", UpdatedAt: s.now()})
	v, _ := s.Snapshot(testContext)
	if !v.Agents[0].LastProgressAt.Equal(first) || !v.Agents[0].LastUpdate.Equal(s.now()) {
		t.Fatal(v.Agents[0])
	}
}
func TestInterruptedSessionCannotBypassResumeViaInstruction(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	start(t, s, a)
	s.UpdateAgent(testContext, a.ID, AgentUpdate{Status: "interrupted", Summary: "Stopped"})
	if err := s.BeginInstruction(testContext, a.ID); err == nil {
		t.Fatal("instruction bypassed bounded recovery")
	}
}
func TestPendingOperationInspectionIsVisibleAndDoesNotReplay(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	a := delegate(t, s, p, "")
	key := "question:" + a.ID + ":fixture"
	s.ClaimEvent(testContext, key)
	v, _ := s.Snapshot(testContext)
	if len(v.PendingOperations) != 1 || v.PendingOperations[0].ProjectID != p.ID || v.PendingOperations[0].Summary == "" {
		t.Fatal(v.PendingOperations)
	}
	if err := s.AcknowledgeEvent(testContext, key, ""); err == nil {
		t.Fatal("empty inspection accepted")
	}
	if err := s.AcknowledgeEvent(testContext, key, "Verified the broker received the question"); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.ClaimEvent(testContext, key)
	if fresh {
		t.Fatal("acknowledgement replayed operation")
	}
}

func TestRefinedSourceContractSurvivesSyncAndFreezesOnCommission(t *testing.T) {
	s, _ := fixture(t)
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Imported issue", Description: "Vague source", AcceptanceCriteria: "Clarify acceptance", SourceID: "linear:refine"})
	if err != nil {
		t.Fatal(err)
	}
	in := DelegateInput{ProjectID: p.ID, ProfileID: "test", Role: "worker", Task: "Implement", AcceptanceCriteria: "Tests pass"}
	if _, err = s.Delegate(testContext, in); err == nil {
		t.Fatal("unrefined import commissioned")
	}
	p, err = s.RefineProject(testContext, p.ID, "Concrete scope", "Search finds fixtures; keyboard navigation passes")
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.CreateProject(testContext, ProjectInput{Title: p.Title, Description: "Refreshed source", AcceptanceCriteria: "Placeholder", SourceID: p.SourceID})
	if err != nil {
		t.Fatal(err)
	}
	if p.Description != "Concrete scope" || p.AcceptanceCriteria != "Search finds fixtures; keyboard navigation passes" || p.SourceDescription != "Refreshed source" {
		t.Fatal(p)
	}
	if _, err = s.Delegate(testContext, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RefineProject(testContext, p.ID, "Different scope", "Different criteria"); err == nil {
		t.Fatal("commissioned contract changed")
	}
}

func TestProjectScopedWorkerCannotCrossProjects(t *testing.T) {
	s, c := fixture(t)
	p := newProject(t, s)
	other := newProject(t, s)
	c.Workers[0].ProjectID = p.ID
	s.UpdateConfig(c)
	if _, err := s.Delegate(testContext, DelegateInput{ProjectID: other.ID, ProfileID: "test", Role: "worker", Task: "Implement", AcceptanceCriteria: "Evidence"}); err == nil {
		t.Fatal("cross-project profile accepted")
	}
	a := delegate(t, s, p, "")
	c.Workers[0].ProjectID = other.ID
	s.UpdateConfig(c)
	if _, err := s.BeginDispatch(testContext, a.ID); err == nil {
		t.Fatal("queued worker bypassed changed profile project scope")
	}
}
