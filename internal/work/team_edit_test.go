package work

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// Two changes to a team made at the same time both stay: neither writes
// back a copy of the team read before the other was saved.
func TestConcurrentTeamEditsBothSurvive(t *testing.T) {
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
