package work

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// Two changes to a team made at the same time both stay: neither writes
// back a copy of the team read before the other was saved.
func TestConcurrentTeamEditsBothSurvive(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "Faster"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 10; round++ {
		target := fmt.Sprintf("release-%d", round)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Go(func() {
			_, errs[0] = a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: target})
		})
		wg.Go(func() {
			_, errs[1] = a.SetParallel(ctx, p.ID, round)
		})
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		snap, err := a.Core.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := findProject(snap, p.ID)
		if got.Playbook.Land.Target != target || got.Playbook.MaxActive != round {
			t.Fatalf("round %d: landing on %q with %d at once, want %q and %d", round, got.Playbook.Land.Target, got.Playbook.MaxActive, target, round)
		}
	}
}

// The owner's stage limits are kept as set, with a stage set to 0 left
// without one, and stay when the team is chosen again; a stage that can't
// have one is refused.
func TestStageLimitsAreSetAndKeptAcrossATeamChange(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "Faster"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	p, err = a.SetStageLimits(ctx, p.ID, map[string]int{core.StageQA: 4, core.StageReviewing: 2, core.StageTodo: 500, core.StageImplementing: 0})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{core.StageQA: 4, core.StageReviewing: 2, core.StageTodo: 500}
	if !maps.Equal(p.Playbook.StageLimits, want) {
		t.Fatalf("limits %v, want %v", p.Playbook.StageLimits, want)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make test"}); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(p.Playbook.StageLimits, want) {
		t.Fatalf("a team change lost the limits: %v", p.Playbook.StageLimits)
	}
	if _, err := a.SetStageLimits(ctx, p.ID, map[string]int{core.StageTriage: 1}); err == nil {
		t.Fatal("Triage was given a limit")
	}
	if p, err = a.SetStageLimits(ctx, p.ID, nil); err != nil || p.Playbook.StageLimits != nil {
		t.Fatalf("clearing the limits: %v %v", p.Playbook.StageLimits, err)
	}
}
