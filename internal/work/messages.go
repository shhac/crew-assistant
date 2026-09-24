package work

import (
	"context"
	"fmt"
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

// answerMessage runs the oldest message to a reviewer or QA: a check of the
// latest revision, with the message in the prompt. It runs ahead of the
// task's own next step, so the owner need not wait for the loop to get there.
// A message sent while the implementer is working waits for the revision it
// is making; one whose role is over its usage threshold waits too.
func (lp *Loop) answerMessage(ctx context.Context, snap core.Snapshot) (bool, error) {
	t, m, ok := nextCheckerMessage(snap)
	if !ok {
		return false, nil
	}
	role, ok := t.Role(m.To)
	if !ok {
		return true, lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, nil, m.To+" is not on this task's team")
	}
	if wait, _ := lp.usageWait(ctx, role); !wait.IsZero() {
		return false, nil
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return false, core.ErrNotFound
	}
	if _, err := lp.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		for i := range t.Messages {
			if t.Messages[i].ID == m.ID && t.Messages[i].Status == core.MessageWaiting {
				t.Messages[i].Status = core.MessageWorking
			}
		}
		return "", nil
	}); err != nil {
		return false, err
	}
	failed := func(err error) (bool, error) {
		return true, lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, nil, err.Error())
	}
	medium, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return failed(err)
	}
	r := t.Revisions[len(t.Revisions)-1]
	verdict, err := lp.runChecker(ctx, p, t, r, role, medium, messageNote(m))
	if err != nil {
		return failed(err)
	}
	verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, role.Name, p.Brief.Version, time.Now().UTC()
	return true, lp.Core.AnswerTeamMessage(ctx, t.ID, m.ID, &verdict, "")
}

// nextCheckerMessage is the oldest open message to a reviewer or QA on a task
// with a revision to check that is not being rewritten.
func nextCheckerMessage(snap core.Snapshot) (core.Task, core.TeamMessage, bool) {
	var task core.Task
	var next core.TeamMessage
	found := false
	for _, t := range snap.Tasks {
		if t.Finished() || t.Status == core.TaskQueued || t.Status == core.TaskWriting || len(t.Revisions) == 0 {
			continue
		}
		for _, m := range t.Messages {
			if m.Kind == core.RoleImplementer || !m.Open() {
				continue
			}
			if !found || m.At.Before(next.At) {
				task, next, found = t, m, true
			}
		}
	}
	return task, next, found
}

func messageNote(m core.TeamMessage) string {
	who := "The owner"
	if m.From == core.FromAssistant {
		who = "The owner's assistant"
	}
	return fmt.Sprintf("\n\n%s asked you for this check directly, saying:\n\n%s\n\nAnswer what they asked in your summary, as well as giving your verdict.", who, m.Text)
}
