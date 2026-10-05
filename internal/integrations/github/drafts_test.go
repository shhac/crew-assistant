package github

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// A draft is never ready to land; it is ready for review once green and
// mergeable, and a check still running isn't green.
func TestADraftIsNeverReadyButCanBeReadyForReview(t *testing.T) {
	green := []Check{{Status: "COMPLETED", Conclusion: "SUCCESS"}}
	draft := PR{State: "OPEN", IsDraft: true, Mergeable: "MERGEABLE", MergeStateStatus: "DRAFT", ReviewDecision: "APPROVED", Checks: green}
	if draft.Ready() || draft.ReadyButThreads() || !draft.ReadyForReview() {
		t.Fatalf("draft: ready %v but threads %v for review %v", draft.Ready(), draft.ReadyButThreads(), draft.ReadyForReview())
	}
	for name, pr := range map[string]PR{
		"running":     {State: "OPEN", IsDraft: true, Mergeable: "MERGEABLE", Checks: []Check{{Status: "IN_PROGRESS"}}},
		"expected":    {State: "OPEN", IsDraft: true, Mergeable: "MERGEABLE", Checks: []Check{{Context: "deploy", State: "EXPECTED"}}},
		"failing":     {State: "OPEN", IsDraft: true, Mergeable: "MERGEABLE", Checks: []Check{{State: "FAILURE"}}},
		"conflicting": {State: "OPEN", IsDraft: true, Mergeable: "CONFLICTING", Checks: green},
		"not a draft": {State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", Checks: green},
	} {
		if pr.ReadyForReview() {
			t.Errorf("%s counted as ready for review", name)
		}
	}
	held := PR{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "APPROVED", Checks: green, Threads: []Thread{{ID: "T1"}}}
	if held.Ready() || !held.ReadyButThreads() {
		t.Fatal("a pull request held only by a thread")
	}
	if (PR{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "BLOCKED", ReviewDecision: "REVIEW_REQUIRED", Checks: green}).ReadyButThreads() {
		t.Fatal("a pull request awaiting review counted as held only by threads")
	}
}

func TestOpenAsADraftAndMarkItReady(t *testing.T) {
	var calls [][]string
	already := errors.New(`gh ready: ! Pull request o/r#42 is already "ready for review": exit status 1`)
	c := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[1] == "ready" && len(calls) > 2 {
			return nil, already
		}
		return []byte("https://github.com/o/r/pull/42\n"), nil
	}}
	if n, _, err := c.Open(context.Background(), "o/r", "main", "crew/x", "T", "B", true); err != nil || n != 42 || calls[0][len(calls[0])-1] != "--draft" {
		t.Fatalf("opened %v %v", calls[0], err)
	}
	if err := c.MarkReady(context.Background(), "o/r", 42); err != nil || !slices.Equal(calls[1], []string{"pr", "ready", "42", "--repo", "o/r"}) {
		t.Fatalf("ready %v %v", calls[1], err)
	}
	if err := c.MarkReady(context.Background(), "o/r", 42); err != nil {
		t.Fatalf("one already ready: %v", err)
	}
	if err := c.MarkReady(context.Background(), "o/r --admin", 42); err == nil {
		t.Fatal("marked a malformed repository's pull request ready")
	}
}

func TestActionsJobsAndLogsAreReadThroughGH(t *testing.T) {
	check := Check{Name: "build", DetailsURL: "https://github.com/O/R/actions/runs/123/job/456?pr=7"}
	if run, job, ok := check.ActionsJob("o/r"); !ok || run != 123 || job != 456 {
		t.Fatalf("run %d job %d %v", run, job, ok)
	}
	for _, other := range []Check{{TargetURL: "https://preview.example/deploy/1"}, {DetailsURL: "https://github.com/someone/else/actions/runs/1/job/2"}} {
		if _, _, ok := other.ActionsJob("o/r"); ok {
			t.Errorf("%+v read as this repository's job", other)
		}
	}
	var calls [][]string
	c := Client{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if strings.HasSuffix(args[len(args)-1], "/logs") {
			return []byte("the log"), nil
		}
		// Paginated, gh prints one page after another.
		return []byte(`{"total_count":3,"jobs":[{"id":1,"name":"a","conclusion":"failure","started_at":"2026-10-05T09:00:00Z"},{"id":2,"name":"b","conclusion":"success"}]}` + "\n" + `{"total_count":3,"jobs":[{"id":3,"name":"c","conclusion":"timed_out","started_at":null}]}`), nil
	}}
	jobs, err := c.RunJobs(context.Background(), "o/r", 123)
	if err != nil || len(jobs) != 3 || !jobs[0].Failed() || jobs[1].Failed() || !jobs[2].Failed() || !jobs[2].StartedAt.IsZero() {
		t.Fatalf("jobs %+v %v", jobs, err)
	}
	if !slices.Equal(calls[0], []string{"api", "--paginate", "repos/o/r/actions/runs/123/jobs?filter=latest&per_page=100"}) {
		t.Fatalf("listed with %v", calls[0])
	}
	if log, err := c.JobLog(context.Background(), "o/r", 1); err != nil || log != "the log" || !slices.Equal(calls[1], []string{"api", "repos/o/r/actions/jobs/1/logs"}) {
		t.Fatalf("log %q %v %v", log, err, calls[1])
	}
	if _, err := c.JobLog(context.Background(), "o/r; rm", 1); err == nil {
		t.Fatal("read a malformed repository's log")
	}
}
