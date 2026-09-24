package github

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestCheckStateReadsTheRollupLikeGitHub(t *testing.T) {
	for _, tc := range []struct {
		checks []Check
		want   string
	}{
		{nil, "NONE"},
		{[]Check{{Status: "COMPLETED", Conclusion: "SUCCESS"}, {State: "SUCCESS"}}, "SUCCESS"},
		{[]Check{{Status: "COMPLETED", Conclusion: "SUCCESS"}, {Status: "IN_PROGRESS"}}, "PENDING"},
		{[]Check{{Status: "IN_PROGRESS"}, {Status: "COMPLETED", Conclusion: "TIMED_OUT"}}, "FAILURE"},
		{[]Check{{State: "PENDING"}}, "PENDING"},
		{[]Check{{State: "ERROR"}}, "FAILURE"},
	} {
		if got := (PR{Checks: tc.checks}).CheckState(); got != tc.want {
			t.Errorf("%+v: got %s want %s", tc.checks, got, tc.want)
		}
	}
}

func TestFeedbackIsWhatArrivedSinceTheTeamLastLookedOldestFirst(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	pr := PR{
		Reviews:  []Review{{Author: Author{"alice"}, State: "CHANGES_REQUESTED", Body: "later", SubmittedAt: t0.Add(2 * time.Minute)}, {Author: Author{"bob"}, State: "COMMENTED", Body: "old", SubmittedAt: t0}},
		Comments: []Comment{{Author: Author{"carol"}, Body: "between", CreatedAt: t0.Add(time.Minute)}},
	}
	got := pr.FeedbackSince(t0)
	if len(got) != 2 || got[0].Body != "between" || got[1].Kind != "review (changes requested)" || !pr.Latest().Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("feedback %+v", got)
	}
}

func TestReadyOnlyWhenApprovedGreenAndUpToDate(t *testing.T) {
	green := []Check{{Status: "COMPLETED", Conclusion: "SUCCESS"}}
	ready := PR{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", Checks: green}
	if !ready.Ready() {
		t.Fatal("an approved, green, clean pull request is not ready")
	}
	for name, pr := range map[string]PR{
		"changes requested": {State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "CHANGES_REQUESTED", Checks: green},
		"behind":            {State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BEHIND", ReviewDecision: "APPROVED", Checks: green},
		"pending checks":    {State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED", Checks: []Check{{Status: "QUEUED"}}},
		"conflicting":       {State: "OPEN", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY", ReviewDecision: "APPROVED", Checks: green},
	} {
		if pr.Ready() {
			t.Errorf("%s counted as ready", name)
		}
	}
	if !(PR{MergeStateStatus: "DIRTY"}).Behind() || !(PR{Mergeable: "CONFLICTING"}).Behind() {
		t.Error("a conflict is not reported as behind")
	}
}

func TestMergeIsPinnedToTheCheckedHeadAndOpenReadsTheNumber(t *testing.T) {
	var calls [][]string
	c := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("https://github.com/o/r/pull/42\n"), nil
	}}
	n, url, err := c.Open(context.Background(), "o/r", "main", "crew/x", "Title", "Body")
	if err != nil || n != 42 || url != "https://github.com/o/r/pull/42" {
		t.Fatalf("opened %d %s %v", n, url, err)
	}
	if err = c.Merge(context.Background(), "o/r", 42, "squash", "abc123"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls[1], []string{"pr", "merge", "42", "--repo", "o/r", "--squash", "--match-head-commit", "abc123"}) {
		t.Fatalf("merge %v", calls[1])
	}
	if c.Merge(context.Background(), "o/r", 42, "force", "abc") == nil {
		t.Fatal("accepted an unknown merge method")
	}
	if _, err = c.View(context.Background(), "not a repo; rm -rf", 1); err == nil {
		t.Fatal("accepted a malformed repository name")
	}
}

func TestAPullRequestRefRoundTripsAndRefusesAnythingElse(t *testing.T) {
	ref, err := ParsePRRef("o/r#12")
	if err != nil || ref != (PRRef{Repo: "o/r", Number: 12}) || ref.String() != "o/r#12" {
		t.Fatalf("ref %+v err %v", ref, err)
	}
	for _, bad := range []string{"o/r", "o/r#0", "o/r#x", "not a repo#1", "#1"} {
		if _, err := ParsePRRef(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
