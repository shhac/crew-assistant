package app

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestAnApprovalStandsOnlyThroughCleanMergesOfTheApprovedDraft(t *testing.T) {
	revs := func(pairs ...[2]int) []core.Revision {
		out := []core.Revision{}
		for _, p := range pairs {
			out = append(out, core.Revision{N: p[0], CleanMergeOf: p[1]})
		}
		return out
	}
	for name, tc := range map[string]struct {
		task core.Task
		want bool
	}{
		"not approved":             {core.Task{Revisions: revs([2]int{1, 0})}, false},
		"the approved draft":       {core.Task{Approved: 1, Revisions: revs([2]int{1, 0})}, true},
		"clean merges of it":       {core.Task{Approved: 1, Revisions: revs([2]int{1, 0}, [2]int{2, 1}, [2]int{3, 2})}, true},
		"a new draft after it":     {core.Task{Approved: 1, Revisions: revs([2]int{1, 0}, [2]int{2, 0})}, false},
		"a chain to another draft": {core.Task{Approved: 2, Revisions: revs([2]int{1, 0}, [2]int{2, 0}, [2]int{3, 1})}, false},
		"a draft naming itself":    {core.Task{Approved: 1, Revisions: revs([2]int{1, 0}, [2]int{2, 2})}, false},
		"a cycle":                  {core.Task{Approved: 1, Revisions: revs([2]int{1, 0}, [2]int{2, 3}, [2]int{3, 2})}, false},
	} {
		if got := approvalStands(tc.task); got != tc.want {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

func TestClipNeverSplitsACharacter(t *testing.T) {
	text := strings.Repeat("é", 10) // two bytes each
	for limit := 1; limit < len(text); limit++ {
		if got := clip(text, limit); !utf8.ValidString(got) {
			t.Fatalf("clip(%d) = %q is not valid UTF-8", limit, got)
		}
	}
	if clip("short", 10) != "short" {
		t.Fatal("clipped text that fit")
	}
}
