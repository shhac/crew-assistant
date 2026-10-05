package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
)

// MessageTeam delivers what the owner or assistant said to one member of a
// task's team, and wakes the loop so it is acted on.
func (lp *Loop) MessageTeam(ctx context.Context, projectID, taskID, to, from, text string) (core.TeamMessage, error) {
	m, err := lp.Core.SendTeamMessage(ctx, projectID, taskID, to, from, text)
	if err == nil {
		lp.Nudge()
	}
	return m, err
}

// answerMessages starts answering open messages to reviewers and QA, oldest
// first: each is a check of the latest revision, with the message in the
// prompt, by the seat it was sent to once that seat is free. They go ahead
// of the tasks' own next steps, so the owner need not wait for the loop to
// get there. A message sent while the implementer is working waits for the
// revision it is making; one whose role is over its usage threshold waits
// too. Each holds its seat while it runs, and no draft replaces the one it
// checks meanwhile.
func (lp *Loop) answerMessages(ctx context.Context, snap core.Snapshot, waited bool) ([]<-chan any, error) {
	var started []<-chan any
	for _, open := range checkerMessages(snap) {
		t, m := open.task, open.message
		if snap.ProjectPaused(t.ProjectID) {
			continue
		}
		if role, ok := t.Role(m.To); ok {
			if wait, _ := lp.usageWait(ctx, role); !wait.IsZero() {
				continue
			}
		}
		taken := &slots{lp: lp}
		s, ok, err := lp.Core.ClaimMessage(ctx, t.ID, m.ID, taken.admit)
		if err != nil {
			taken.giveBack()
			return started, err
		}
		if ok {
			started = append(started, lp.launch(ctx, s, waited))
		}
	}
	return started, nil
}

// answerMessage runs the check a claimed message asks for and answers it.
func (lp *Loop) answerMessage(ctx context.Context, p core.Project, t core.Task, s core.Scheduled) error {
	i := slices.IndexFunc(t.Messages, func(m core.TeamMessage) bool { return m.ID == s.Claim.Message })
	if i < 0 || !t.Messages[i].Open() {
		return nil
	}
	m := t.Messages[i]
	if s.Seat.Name == "" {
		return lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, nil, core.Screenshots{}, m.To+" is not on this task's team")
	}
	if m.ForPR {
		return lp.answerOnPR(ctx, p, t, s, m)
	}
	return lp.answerCheck(ctx, p, t, s, m)
}

// answerCheck answers a message to a reviewer or QA with a check of the
// latest revision.
func (lp *Loop) answerCheck(ctx context.Context, p core.Project, t core.Task, s core.Scheduled, m core.TeamMessage) error {
	failed := func(err error) error {
		return lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, nil, core.Screenshots{}, err.Error())
	}
	medium, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return failed(err)
	}
	r := t.Revisions[len(t.Revisions)-1]
	verdict, shots, end, err := lp.runChecker(ctx, p, t, r, s.Seat, medium, messageNote(m))
	cleanupErr := err
	if err != nil && (!errors.Is(err, errCommandRecovery) || verdict.Ref == "") {
		reported := failed(err)
		if errors.Is(err, errCommandRecovery) {
			return errors.Join(err, reported)
		}
		return reported
	}
	// Ref is what the checker's own copy held, set with the verdict.
	verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, s.Seat.Name, p.Brief.Version, time.Now().UTC()
	// The verdict judged the text the checker was shown, not whatever it
	// became while the checker worked.
	verdict.TextVersion = t.TextVersion
	err = lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, &verdict, shots, "", end)
	if errors.Is(cleanupErr, errCommandRecovery) && t.AcceptanceStands(p.Brief.Version) && t.MergeValidation != nil {
		err = errors.Join(err, lp.acceptedMergeFailure(ctx, t, s.Seat.Name+" cleanup is unconfirmed: "+cleanupErr.Error()))
	}
	return errors.Join(cleanupErr, err)
}

type openMessage struct {
	task    core.Task
	message core.TeamMessage
}

// checkerMessages are the open messages to reviewers or QA on tasks with a
// revision to check that is not being rewritten, oldest first.
func checkerMessages(snap core.Snapshot) []openMessage {
	var out []openMessage
	for _, t := range snap.Tasks {
		if t.Finished() || t.Status == core.TaskQueued || t.Status == core.TaskWriting || len(t.Revisions) == 0 {
			continue
		}
		for _, m := range t.Messages {
			if m.Kind != core.RoleImplementer && m.Open() {
				out = append(out, openMessage{t, m})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b openMessage) int { return a.message.At.Compare(b.message.At) })
	return out
}

func messageNote(m core.TeamMessage) string {
	if m.ForPR {
		return fmt.Sprintf("\n\n%s, the implementer, handed you this about the change's open pull request on GitHub:\n\n%s\n\nYour summary is posted on the pull request as your reply, signed with your name: write it for the people reviewing the pull request, as well as giving your verdict.", m.From, m.Text)
	}
	who := "The owner"
	if m.From == core.FromAssistant {
		who = "The owner's assistant"
	}
	return fmt.Sprintf("\n\n%s asked you for this check directly, saying:\n\n%s\n\nAnswer what they asked in your summary, as well as giving your verdict.", who, m.Text)
}

// answerOnPR has a teammate answer what the implementer handed it about the
// pull request, and posts the answer there at once: a reviewer or QA with a
// check, the PM with what it decides.
func (lp *Loop) answerOnPR(ctx context.Context, p core.Project, t core.Task, s core.Scheduled, m core.TeamMessage) error {
	var err error
	if m.Kind == core.RolePM {
		err = lp.answerPRAsPM(ctx, p, t, s.Seat, m)
	} else {
		err = lp.answerCheck(ctx, p, t, s, m)
	}
	if err != nil || !t.PROpen() || t.Playbook == nil {
		return err
	}
	return lp.postOutbox(ctx, t.ID, t.Playbook.Land.GitHub, t.Proposal.Number)
}
