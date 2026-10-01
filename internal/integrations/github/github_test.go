package github

import (
	"context"
	"encoding/json"
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

// A review keeps the head it was made on, as gh reports it; a comment is on
// no commit.
func TestAReviewKeepsTheCommitItWasMadeOn(t *testing.T) {
	var pr PR
	raw := `{"reviews":[{"author":{"login":"alice"},"state":"COMMENTED","body":"old push","submittedAt":"2026-09-24T09:01:00Z","commit":{"oid":"abc123"}}],"comments":[{"author":{"login":"bob"},"body":"hi","createdAt":"2026-09-24T09:02:00Z"}]}`
	if err := json.Unmarshal([]byte(raw), &pr); err != nil {
		t.Fatal(err)
	}
	got := pr.FeedbackSince(time.Time{})
	if len(got) != 2 || got[0].Commit != "abc123" || got[1].Commit != "" {
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

// View reads the review threads gh's pull request view leaves out, and an
// unresolved one keeps the pull request from being ready.
func TestViewReadsReviewThreadsAndAnUnresolvedOneHoldsReadiness(t *testing.T) {
	threads := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
		{"id":"T1","isResolved":false,"isOutdated":false,"path":"main.go","line":12,"comments":{"nodes":[{"author":{"login":"alice"},"authorAssociation":"COLLABORATOR","body":"Rename this?","createdAt":"2026-10-01T09:05:00Z"}]}},
		{"id":"T2","isResolved":true,"path":"a.go","line":1,"comments":{"nodes":[{"author":{"login":"bob"},"authorAssociation":"NONE","body":"done","createdAt":"2026-10-01T09:00:00Z"}]}}]}}}}}`
	var asked [][]string
	c := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		asked = append(asked, args)
		if args[0] == "api" {
			return []byte(threads), nil
		}
		return []byte(`{"number":7,"state":"OPEN","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}],"comments":[{"author":{"login":"eve"},"authorAssociation":"NONE","body":"spam","createdAt":"2026-10-01T09:01:00Z"}]}`), nil
	}}
	pr, err := c.View(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Threads) != 2 || pr.Unresolved() != 1 || pr.Ready() {
		t.Fatalf("threads %+v unresolved %d ready %v", pr.Threads, pr.Unresolved(), pr.Ready())
	}
	if !slices.Contains(asked[1], "owner=o") || !slices.Contains(asked[1], "name=r") || !slices.Contains(asked[1], "number=7") {
		t.Fatalf("graphql %v", asked[1])
	}
	feedback := pr.FeedbackSince(time.Date(2026, 10, 1, 9, 0, 30, 0, time.UTC))
	if len(feedback) != 2 || feedback[0].Body != "spam" || feedback[0].Trusted() || feedback[1].Thread != "T1" || feedback[1].Path != "main.go" || feedback[1].Line != 12 || !feedback[1].Trusted() {
		t.Fatalf("feedback %+v", feedback)
	}
	if !pr.Latest().Equal(time.Date(2026, 10, 1, 9, 5, 0, 0, time.UTC)) {
		t.Fatalf("latest %v", pr.Latest())
	}
	pr.Threads[0].Resolved = true
	if !pr.Ready() {
		t.Fatal("green, clean, no review required and every thread resolved is not ready")
	}
}

func TestRepliesResolvesCommentsAndEditsNameWhatTheyAct(t *testing.T) {
	var calls [][]string
	c := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return []byte("{}"), nil
	}}
	ctx := context.Background()
	if err := c.Reply(ctx, "PRRT_x", "Renamed."); err != nil {
		t.Fatal(err)
	}
	if err := c.Resolve(ctx, "PRRT_x"); err != nil {
		t.Fatal(err)
	}
	if err := c.Comment(ctx, "o/r", 7, "Thanks"); err != nil {
		t.Fatal(err)
	}
	if err := c.Edit(ctx, "o/r", 7, "Title", "Body"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls[0], "thread=PRRT_x") || !slices.Contains(calls[0], "body=Renamed.") || !slices.Contains(calls[1], "thread=PRRT_x") {
		t.Fatalf("thread calls %v", calls[:2])
	}
	if !slices.Equal(calls[2], []string{"pr", "comment", "7", "--repo", "o/r", "--body", "Thanks"}) || !slices.Equal(calls[3], []string{"pr", "edit", "7", "--repo", "o/r", "--title", "Title", "--body", "Body"}) {
		t.Fatalf("calls %v", calls[2:])
	}
	if c.Reply(ctx, "x\"; y", "b") == nil || c.Resolve(ctx, "") == nil || c.Comment(ctx, "bad repo", 1, "b") == nil {
		t.Fatal("accepted a malformed target")
	}
}
