package core

import (
	"errors"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func linearFixture(t *testing.T) (*Service, Project, LinearLink) {
	t.Helper()
	s, cfg := fixture(t)
	cfg.Connections = []config.Connection{{ID: "lin", Name: "Linear", Tool: "lin", Profiles: []string{"home"}}}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, s)
	l := LinearLink{ConnectionID: "lin", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Team", Rules: LinearRules{PickUp: true, Assignee: "unassigned", States: []string{"Todo"}}}
	var err error
	p, err = s.SetProjectLinear(testContext, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	return s, p, *p.Linear
}

func TestLinearUnchangedUpdatesDoNotRewriteState(t *testing.T) {
	s, p, l := linearFixture(t)
	issue := LinearIssue{ID: "issue", Identifier: "EX-1", Title: "Work", URL: "https://linear.app/issue/EX-1"}
	if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err != nil {
		t.Fatal(err)
	}
	if err := s.LinearError(testContext, p.ID, l.Version, "offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectLinearCursor(testContext, p.ID, l.Version, "next"); err != nil {
		t.Fatal(err)
	}
	version := func() int {
		var n int
		if err := s.store.db.QueryRow("SELECT version FROM state WHERE id=1").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := version()
	if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err != nil {
		t.Fatal(err)
	}
	if err := s.LinearError(testContext, p.ID, l.Version, "offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinearError(testContext, p.ID, l.Version-1, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetProjectLinearCursor(testContext, p.ID, l.Version, "next"); err != nil {
		t.Fatal(err)
	}
	if version() != before {
		t.Fatal("unchanged operations rewrote state")
	}
}
func TestLinearPickUpAtomicAndKeepsSourceAfterUnlink(t *testing.T) {
	s, p, l := linearFixture(t)
	issue := LinearIssue{ID: "issue", Identifier: "EX-1", Title: "Do the work", URL: "https://linear.app/example/issue/EX-1", Description: "Keep empty exports readable."}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	snap, _ := s.Snapshot(testContext)
	if len(snap.Tasks) != 1 || len(snap.Projects[0].LinearImported) != 1 || snap.Tasks[0].Linear[0].URL != issue.URL || snap.Tasks[0].Status != TaskQueued {
		t.Fatalf("not atomic: %+v", snap)
	}
	count := 0
	for _, a := range snap.Activity {
		if a.Kind == "task.picked-up" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("activity: %d", count)
	}
	if snap.Tasks[0].Linear[0].Description != issue.Description || len(snap.Tasks[0].Criteria) != 0 {
		t.Fatal("source context was lost or made a criterion", snap.Tasks[0])
	}
	finish(t, s, snap.Tasks[0].ID, TaskStopped)
	if _, err := s.SetProjectLinear(testContext, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.SetProjectLinear(testContext, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	if p.Linear.Version <= l.Version {
		t.Fatal("version reused")
	}
	if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); !errors.Is(err, ErrConflict) {
		t.Fatal("stale read accepted", err)
	}
	if made, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, issue); err != nil || made {
		t.Fatal("reimported after relink", made, err)
	}
	snap, _ = s.Snapshot(testContext)
	if len(snap.Tasks[0].Linear) != 1 {
		t.Fatal("unlink lost source")
	}
}

func TestLinearReceiptsSurviveConnectionAndAccountChanges(t *testing.T) {
	s, p, l := linearFixture(t)
	issue := LinearIssue{ID: "issue", Identifier: "EX-1", Title: "Work", URL: "https://linear.app/issue/EX-1"}
	if made, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err != nil || !made {
		t.Fatal(made, err)
	}
	cfg := s.configuration()
	cfg.Connections = []config.Connection{{ID: "replacement", Name: "Linear", Tool: "lin", Profiles: []string{"new-alias"}}}
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	l.ConnectionID, l.Profile = "replacement", "new-alias"
	p, err := s.SetProjectLinear(testContext, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	if made, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, issue); err != nil || made {
		t.Fatal("issue reimported through another connection/account", made, err)
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Tasks) != 1 || len(snap.Projects[0].LinearImported) != 1 || snap.Projects[0].LinearImported[0] != issue.ID {
		t.Fatal(snap)
	}
	if snap.Tasks[0].Linear[0].ConnectionID != "lin" {
		t.Fatal("original provenance rewritten")
	}
}

func TestLinearRecoveryActivityAndStaleCursor(t *testing.T) {
	s, p, l := linearFixture(t)
	for _, message := range []string{"Read failed", "Read failed", "", ""} {
		if err := s.LinearError(testContext, p.ID, l.Version, message); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	entries := []string{}
	for _, a := range snap.Activity {
		if a.Kind == "project.linear-status" {
			entries = append(entries, a.Summary)
		}
	}
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	for _, message := range entries {
		if message == "" {
			t.Fatal("empty recovery")
		}
	}
	if err := s.SetProjectLinearCursor(testContext, p.ID, l.Version, "cursor"); err != nil {
		t.Fatal(err)
	}
	p, err := s.SetProjectLinear(testContext, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	if p.Linear.Cursor != "" {
		t.Fatal("new rules retained old cursor")
	}
	if err := s.SetProjectLinearCursor(testContext, p.ID, l.Version, "old cursor"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale cursor accepted", err)
	}
}
func TestLinearLinkValidationAndReadiness(t *testing.T) {
	s, p, l := linearFixture(t)
	cfg := s.configuration()
	cfg.Connections = append(cfg.Connections, config.Connection{ID: "slack", Name: "Slack", Tool: "agent-slack", Profiles: []string{"home"}})
	if err := s.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*LinearLink){func(l *LinearLink) { l.ConnectionID = "missing" }, func(l *LinearLink) { l.ConnectionID = "slack" }, func(l *LinearLink) { l.Profile = "other" }, func(l *LinearLink) { l.ID = "" }, func(l *LinearLink) { l.Rules.Assignee = "users" }} {
		bad := l
		mutate(&bad)
		if _, err := s.SetProjectLinear(testContext, p.ID, &bad); err == nil {
			t.Fatal("bad link accepted", bad)
		}
	}
	issue := LinearIssue{ID: "issue", Identifier: "EX-1", Title: "Work", URL: "https://linear.app/issue/EX-1"}
	if err := s.store.update(testContext, func(v *Snapshot) error { project(v, p.ID).Brief.Goal = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err == nil {
		t.Fatal("no brief accepted")
	}
	snap, _ := s.Snapshot(testContext)
	if len(snap.Projects[0].LinearImported) != 0 {
		t.Fatal("failure marked imported")
	}
	l.Rules.PickUp = false
	p, err := s.SetProjectLinear(testContext, p.ID, &l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PickUpLinearIssue(testContext, p.ID, p.Linear.Version, issue); !errors.Is(err, ErrConflict) {
		t.Fatal("disabled accepted", err)
	}
}

func TestLinearImportedTaskGoesToPM(t *testing.T) {
	s, p, l := linearFixture(t)
	pb := *p.Playbook
	pb.Roles = append(pb.Roles, Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude"})
	if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	issue := LinearIssue{ID: "issue", Identifier: "EX-1", Title: "Work", URL: "https://linear.app/issue/EX-1"}
	if _, err := s.PickUpLinearIssue(testContext, p.ID, l.Version, issue); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if snap.Tasks[0].Status != TaskTriage {
		t.Fatal("skipped PM", snap.Tasks[0])
	}
}
