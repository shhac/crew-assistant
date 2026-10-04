package work

import (
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestAnImplementersReplyIsTakenApartIntoItsBlocks(t *testing.T) {
	t.Parallel()
	text := "Did it.\n```learned\nL\n```\n```wake\nW\n```\n```owner-step\nO\n```\n```pr-reply\nR\n```\n```pr\nP\n```\n```design\nD\n```"
	got := parseWriterReply(text, true)
	want := writerReply{reply: "Did it.", learned: "L", wake: "W", unmet: "O", prReply: "R", pr: "P", question: "D"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	// Only an implementer who may ask the designer has its design block read.
	if got = parseWriterReply(text, false); got.question != "" || got.pr != "P" {
		t.Fatalf("%+v", got)
	}
}

// A round on an open pull request that changed nothing posts its replies
// with the draft the pull request has, and lands again, unless direction
// came while it ran, which it revises with at once.
func TestARoundThatChangedNothingRepliesWithTheDraftThePullRequestHas(t *testing.T) {
	t.Parallel()
	task := core.Task{Status: core.TaskWriting, Round: 2, MaxRounds: 5, Revisions: []core.Revision{{N: 1}, {N: 2}}, WriterRequest: 3, WriterNext: "x"}
	h := core.Handoff{Reply: "Nothing to change.", Request: 3, WakeErrors: []string{"w"}, Posts: []core.PRPost{{Thread: "T1", Body: "As intended.", Revision: 3}}}
	noChangeNeeded(&task, h)
	if task.Status != core.TaskLanding || task.WriterNext != "" || len(task.WakeErrors) != 1 || len(task.Proposal.Outbox) != 1 || task.Proposal.Outbox[0].Revision != 2 {
		t.Fatalf("%s next %q outbox %+v", task.Status, task.WriterNext, task.Proposal)
	}
	directed := core.Task{Status: core.TaskWriting, Round: 2, MaxRounds: 5, Revisions: []core.Revision{{N: 1}}, Direction: []string{"also this"}, DirectionPending: 1}
	noChangeNeeded(&directed, core.Handoff{Reply: "Nothing to change."})
	if directed.Status != core.TaskWriting || directed.Round != 3 {
		t.Fatalf("direction not taken in: %s round %d", directed.Status, directed.Round)
	}
}
