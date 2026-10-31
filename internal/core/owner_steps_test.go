package core

import (
	"strings"
	"testing"
)

// The team may reword or drop any requirement it was given; an approval says
// which of the first ones it no longer checks, unless the owner took them out
// or kept them as checks of their own.
func TestDroppedRequirementsNameWhatTheTeamTookOut(t *testing.T) {
	task := Task{Criteria: []string{"Handles errors", "Fast", "Logs clearly"}}
	if task.DroppedRequirements() != "" {
		t.Fatal("an unedited task dropped something")
	}
	task.Edits = []TaskEdit{
		{Kind: RoleResearcher, Before: TaskText{Criteria: []string{"Handles errors", "Fast", "Logs clearly", "Works offline", "Documented"}}, After: TaskText{Criteria: []string{"Handles errors", "Fast enough for 1k rows", "Logs clearly", "Documented"}}},
		{Kind: FromOwner, Before: TaskText{Criteria: []string{"Handles errors", "Fast enough for 1k rows", "Logs clearly", "Documented"}}, After: TaskText{Criteria: []string{"Handles errors", "Fast enough for 1k rows", "Logs clearly"}}},
	}
	task.Criteria = []string{"Handles errors", "Fast enough for 1k rows"}
	task.OwnerChecks = []string{"Logs clearly"}
	got := task.DroppedRequirements()
	for _, want := range []string{"\n- Fast\n", "\n- Works offline\n", "Undo the team's change"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"Handles errors", "Documented", "Logs clearly"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("should not name %q: %s", unwanted, got)
		}
	}
}
