//go:build !windows

package work

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

// The implementer answers a thread that needs no change on the pull request
// itself, signed, and resolves it; nothing it posts is taken for feedback.
func TestTheImplementerAnswersAThreadOnThePullRequest(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 2)
	s.open(t)
	s.runner.onEdit = func(_ string, n int) bool { return n == 1 }
	s.runner.ending = func(n int) string {
		if n == 1 {
			return ""
		}
		return "\n```pr-reply\n{\"replies\": [{\"thread\": \"T1\", \"body\": \"It matches the API.\"}], \"resolve\": [\"T1\"]}\n```"
	}
	at := time.Now()
	s.gh.set(func() { s.gh.threads = []map[string]any{thread("T1", "Why this name?", at)} })
	if err := s.a.checkWakes(s.ctx, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	task := s.current(t)
	replies, resolves := s.gh.postsOf("reply"), s.gh.postsOf("resolve")
	if len(replies) != 1 || !slices.Contains(replies[0], "thread=T1") || !slices.ContainsFunc(replies[0], func(a string) bool {
		return strings.HasPrefix(a, "body=It matches the API.") && strings.Contains(a, "— Implementer, for crew-assistant") && strings.Contains(a, ownPost)
	}) {
		t.Fatalf("replies %v", replies)
	}
	if len(resolves) != 1 || !slices.Contains(resolves[0], "thread=T1") {
		t.Fatalf("resolves %v", resolves)
	}
	if len(task.Revisions) != 1 || len(task.Proposal.Outbox) != 0 || task.Status != core.TaskAwaiting {
		t.Fatalf("after answering: %s revisions %d outbox %+v", task.Status, len(task.Revisions), task.Proposal.Outbox)
	}
}

// A question about trying the product can go to QA, whose answer is posted
// on the pull request in its own name.
func TestTheImplementerHandsAPullRequestQuestionToQA(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 3)
	s.open(t)
	s.runner.onEdit = func(_ string, n int) bool { return n == 1 }
	s.runner.ending = func(n int) string {
		if n == 1 {
			return ""
		}
		return "\n```pr-reply\n{\"hand_to\": {\"to\": \"QA\", \"question\": \"Does the feature still pass make check on a clean clone?\"}}\n```"
	}
	s.review(t, "Did anyone try this on a clean clone?", time.Now())
	task := s.current(t)
	asked := slices.IndexFunc(task.Messages, func(m core.TeamMessage) bool { return m.ForPR && m.To == "QA" })
	if asked < 0 || task.Messages[asked].Status != core.MessageAnswered || task.Messages[asked].From != "Implementer" {
		t.Fatalf("messages %+v", task.Messages)
	}
	comments := s.gh.postsOf("comment")
	if len(comments) != 1 || !strings.Contains(argAfter(comments[0], "--body"), "— QA, for crew-assistant") {
		t.Fatalf("comments %v", comments)
	}
}

// Handed to the PM, a pull request question can be answered there and put
// to the owner.
func TestThePMAnswersAPullRequestQuestionAndAsksTheOwner(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 2, withPM)
	s.open(t)
	s.runner.onEdit = func(_ string, n int) bool { return n == 1 }
	s.runner.ending = func(n int) string {
		if n == 1 {
			return ""
		}
		return "\n```pr-reply\n{\"hand_to\": {\"to\": \"pm\", \"question\": \"The reviewer wants this behind a flag; is that in scope?\"}}\n```"
	}
	s.runner.mu.Lock()
	s.runner.pmOnPR = []string{`{"reply": "Thanks; checking scope with the owner.", "ask_owner": "Should the feature ship behind a flag?"}`}
	s.runner.mu.Unlock()
	s.review(t, "Put this behind a flag.", time.Now())
	task := s.current(t)
	comments := s.gh.postsOf("comment")
	if len(comments) != 1 || !strings.Contains(argAfter(comments[0], "--body"), "Thanks; checking scope with the owner.\n\n— Pim, for crew-assistant") {
		t.Fatalf("comments %v", comments)
	}
	snap, _ := s.a.Core.Snapshot(s.ctx)
	asked := slices.IndexFunc(snap.Decisions, func(d core.Decision) bool {
		return d.TaskID == s.id && d.Kind == core.DecisionQuestion && strings.Contains(d.Context, "behind a flag")
	})
	if asked < 0 || task.Status != core.TaskWaiting {
		t.Fatalf("no question for the owner: %s %+v", task.Status, snap.Decisions)
	}
}

// Replying in a thread makes GitHub add an empty comment-only review from
// the same account; neither it nor the reply is feedback, so the team's own
// answer never starts another round.
func TestTheTeamsOwnThreadReplyStartsNoRound(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 2)
	s.open(t)
	s.runner.onEdit = func(_ string, n int) bool { return n == 1 }
	s.runner.ending = func(n int) string {
		if n == 1 {
			return ""
		}
		return "\n```pr-reply\n{\"replies\": [{\"thread\": \"T1\", \"body\": \"Yes, on purpose.\"}]}\n```"
	}
	at := time.Now()
	s.gh.set(func() { s.gh.threads = []map[string]any{thread("T1", "Is this on purpose?", at)} })
	if err := s.a.checkWakes(s.ctx, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.current(t)
	// What GitHub then shows: the reply in the thread, under the owner's
	// login, and an empty review wrapping it.
	replied := at.Add(2 * time.Minute)
	s.gh.set(func() {
		nodes := s.gh.threads[0]["comments"].(map[string]any)["nodes"].([]map[string]any)
		s.gh.threads[0]["comments"] = map[string]any{"nodes": append(nodes, map[string]any{"author": map[string]string{"login": "owner"}, "authorAssociation": "OWNER", "body": "Yes, on purpose.\n\n— Implementer, for crew-assistant\n" + ownPost, "createdAt": replied.Format(time.RFC3339)})}
		s.gh.reviews = append(s.gh.reviews, github.Review{Author: github.Author{Login: "owner"}, Association: "OWNER", State: "COMMENTED", SubmittedAt: replied})
	})
	if err := s.a.checkWakes(s.ctx, replied.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	task := s.current(t)
	if s.runner.edits != 2 || len(s.gh.postsOf("reply")) != 1 || task.Status != core.TaskAwaiting {
		t.Fatalf("its own reply started another round: edits %d replies %d status %s", s.runner.edits, len(s.gh.postsOf("reply")), task.Status)
	}
}

// A reply posted in the same look as new feedback arrives is posted once:
// answering the feedback never puts back what was already posted.
func TestAPostedReplyIsNotPostedAgainWhenFeedbackArrivesInTheSameLook(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 4)
	s.open(t)
	if _, err := s.a.Core.UpdateTask(s.ctx, s.id, func(t *core.Task, _ *core.Project) (string, error) {
		t.Post(core.PRPost{Body: "Checked on a clean clone.", By: "QA"})
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	s.review(t, "Handle the nil case.", time.Now())
	task := s.current(t)
	if comments := s.gh.postsOf("comment"); len(comments) != 1 {
		t.Fatalf("posted %d times: %v", len(comments), comments)
	}
	if len(task.Proposal.Outbox) != 0 {
		t.Fatalf("outbox %+v", task.Proposal.Outbox)
	}
}

// Replies that came with a draft wait for it to be pushed, even when a
// teammate's answer is posted first, so a thread is never resolved for a fix
// a reviewer then turns down.
func TestRepliesWithADraftWaitForItsPushEvenWhenQAAnswersFirst(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 4)
	s.open(t)
	snap, _ := s.a.Core.Snapshot(s.ctx)
	task, _ := snap.FindTask(s.id)
	if _, err := s.a.Core.UpdateTask(s.ctx, s.id, func(t *core.Task, _ *core.Project) (string, error) {
		t.Post(core.PRPost{Thread: "T1", Body: "Fixed in the next draft.", By: "Implementer", Revision: len(t.Revisions) + 1},
			core.PRPost{Thread: "T1", Resolve: true, By: "Implementer", Revision: len(t.Revisions) + 1},
			core.PRPost{Body: "It passes on a clean clone.", By: "QA"})
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.a.postOutbox(s.ctx, s.id, "o/r", task.Proposal.Number); err != nil {
		t.Fatal(err)
	}
	if len(s.gh.postsOf("comment")) != 1 || len(s.gh.postsOf("reply")) != 0 || len(s.gh.postsOf("resolve")) != 0 {
		t.Fatalf("posted before the draft was pushed: comments %v replies %v resolves %v", s.gh.postsOf("comment"), s.gh.postsOf("reply"), s.gh.postsOf("resolve"))
	}
	if task = s.current(t); len(task.Proposal.Outbox) != 2 {
		t.Fatalf("outbox %+v", task.Proposal.Outbox)
	}
}

// A reply GitHub keeps refusing, such as one to a thread that doesn't exist,
// is given up after a few looks rather than holding the pull request.
func TestAReplyGitHubKeepsRefusingIsGivenUp(t *testing.T) {
	t.Parallel()
	s := newPRScenario(t, 2)
	s.open(t)
	run := s.a.github.Run
	s.a.github.Run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "api" && strings.Contains(args[3], "addPullRequestReviewThreadReply") {
			return nil, fmt.Errorf("no such thread")
		}
		return run(ctx, args...)
	}
	if _, err := s.a.Core.UpdateTask(s.ctx, s.id, func(t *core.Task, _ *core.Project) (string, error) {
		t.Post(core.PRPost{Thread: "T9", Body: "Answered.", By: "Implementer"}, core.PRPost{Body: "And this.", By: "Implementer"})
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	for look := 1; look <= maxPostFailures; look++ {
		if err := s.a.postOutbox(s.ctx, s.id, "o/r", 7); err != nil {
			t.Fatal(err)
		}
		task := s.current(t)
		if look < maxPostFailures && (len(task.Proposal.Outbox) != 2 || task.Proposal.Outbox[0].Failures != look) {
			t.Fatalf("look %d: outbox %+v", look, task.Proposal.Outbox)
		}
	}
	task := s.current(t)
	if len(task.Proposal.Outbox) != 0 || len(s.gh.postsOf("comment")) != 1 || !activityHas(t, s.a, "Gave up posting Implementer's reply on pull request #7") {
		t.Fatalf("outbox %+v comments %v", task.Proposal.Outbox, s.gh.postsOf("comment"))
	}
}
