package work

import (
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

// A pr or pr-reply block the implementer got wrong is told back to it, and
// nothing half-read is acted on.
func TestPRBlocksTellTheImplementerWhatWasWrong(t *testing.T) {
	t.Parallel()
	for block, want := range map[string]string{
		`not json`:                     "not valid JSON",
		`{"title": "  ", "body": "b"}`: `needs a "title" and a "body"`,
		`{"title": "T", "body": ""}`:   `needs a "title" and a "body"`,
	} {
		if text, problem := parsePRText(block); text != nil || !strings.Contains(problem, want) {
			t.Errorf("%s: %+v %q", block, text, problem)
		}
	}
	if text, problem := parsePRText(`{"title": "  A  title\nhere ", "body": " B "}`); problem != "" || text.Title != "A title here" || text.Body != "B" {
		t.Errorf("not tidied: %+v %q", text, problem)
	}
	if posts, hand, problems := parsePRReply(`{"replies": [{"body": "x"}]}`, "Ada", false, 2); posts != nil || hand != nil || len(problems) != 1 {
		t.Errorf("replied on no pull request: %+v %v", posts, problems)
	}
	posts, hand, problems := parsePRReply(`{"replies": [{"thread": "T1", "body": " "}, {"body": "ok"}], "resolve": ["T1", " "], "hand_to": {"to": "QA"}}`, "Ada", true, 2)
	if len(posts) != 2 || posts[0].Body != "ok" || !posts[1].Resolve || posts[1].Revision != 2 || hand != nil || len(problems) != 2 {
		t.Errorf("posts %+v hand %+v problems %v", posts, hand, problems)
	}
}

// What the loop records of a pull request: only an open, mergeable one with
// every thread resolved and checks done is ready, and feedback that says
// nothing isn't counted as ignored.
func TestWhatTheLoopObservesOfAPullRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	green := []github.Check{{Status: "COMPLETED", Conclusion: "SUCCESS"}}
	ready := github.PR{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", Checks: green}
	pushed := core.Proposal{PushedAt: now.Add(-time.Minute)}
	if o := observed(ready, pushed, core.LandPolicy{}, now); !o.Ready || o.Checks != "SUCCESS" {
		t.Fatalf("a green, clean pull request: %+v", o)
	}
	for name, pr := range map[string]github.PR{
		"merged":  {State: "MERGED", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", Checks: green},
		"unknown": {State: "OPEN", Mergeable: "UNKNOWN", MergeStateStatus: "UNKNOWN", Checks: green},
	} {
		if o := observed(pr, pushed, core.LandPolicy{}, now); o.Ready || o.Conflicting {
			t.Errorf("%s: %+v", name, o)
		}
	}
	if o := observed(github.PR{State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "DIRTY", Checks: green}, pushed, core.LandPolicy{}, now); !o.Conflicting || o.Ready {
		t.Errorf("dirty: %+v", o)
	}
	// Checks GitHub reports are taken as they are, however recent the push.
	failing := ready
	failing.Checks = []github.Check{{Status: "COMPLETED", Conclusion: "FAILURE"}}
	if o := observed(failing, pushed, core.LandPolicy{}, now); o.Checks != "FAILURE" || o.Ready {
		t.Errorf("failing just after a push: %+v", o)
	}
	said := ready
	said.Reviews = []github.Review{{Association: "NONE", State: "COMMENTED", SubmittedAt: now}}
	said.Comments = []github.Comment{{Association: "NONE", Body: "Our reply.\n" + ownPost, CreatedAt: now}, {Association: "NONE", Body: "Please change it.", CreatedAt: now}}
	if o := observed(said, pushed, core.LandPolicy{}, now); o.Ignored != 1 {
		t.Errorf("ignored %d, want only the outsider's real comment", o.Ignored)
	}
}
