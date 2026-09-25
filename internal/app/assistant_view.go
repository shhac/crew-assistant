package app

import (
	"slices"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// shownFinished is how many finished tasks the assistant sees each turn; it
// reads any other with read_task.
const shownFinished = 12

// assistantView is the state the assistant reads each turn: everything it can
// act on, and only the outcome of what is finished. Full revision and review
// history stays on the project pages, and read_task brings one task's; carrying
// it into every turn crowded out the conversation itself, and in time no
// longer fitted at all.
func assistantView(s core.Snapshot) core.Snapshot {
	recent := recentlyFinished(s.Tasks, shownFinished)
	tasks := make([]core.Task, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		if t.Finished() {
			if recent[t.ID] {
				tasks = append(tasks, finishedBrief(t))
			}
			continue
		}
		t.WriterSession, t.Playbook, t.Roles = nil, nil, nil
		if t.Plan != nil {
			t.Plan = clippedPlan(*t.Plan)
		}
		if n := len(t.Revisions); n > 2 {
			t.Revisions = t.Revisions[n-2:]
		}
		revisions := make([]core.Revision, len(t.Revisions))
		for i, r := range t.Revisions {
			r.Summary = text.Clip(r.Summary, 500)
			if len(r.Files) > 20 {
				r.Files = r.Files[:20]
			}
			revisions[i] = r
		}
		t.Revisions = revisions
		var verdicts []core.Verdict
		if len(revisions) > 0 {
			latest := revisions[len(revisions)-1].N
			for _, v := range t.Verdicts {
				if v.Revision != latest {
					continue
				}
				v.Summary = text.Clip(v.Summary, 400)
				findings := make([]core.Finding, len(v.Findings))
				for i, f := range v.Findings {
					f.Note = text.Clip(f.Note, 300)
					findings[i] = f
				}
				v.Findings = findings
				verdicts = append(verdicts, v)
			}
		}
		t.Verdicts = verdicts
		if n := len(t.Messages); n > 5 {
			t.Messages = t.Messages[n-5:]
		}
		messages := make([]core.TeamMessage, len(t.Messages))
		for i, m := range t.Messages {
			m.Text, m.Reply = text.Clip(m.Text, 300), text.Clip(m.Reply, 300)
			messages[i] = m
		}
		t.Messages = messages
		tasks = append(tasks, t)
	}
	s.Tasks = tasks

	var open, closed []core.Decision
	for _, d := range s.Decisions {
		if d.Status == "open" {
			open = append(open, d)
			continue
		}
		d.Context = text.Clip(d.Context, 300)
		closed = append(closed, d)
	}
	if len(closed) > 8 {
		closed = closed[len(closed)-8:]
	}
	s.Decisions = append(closed, open...)

	if len(s.Activity) > 30 {
		s.Activity = s.Activity[len(s.Activity)-30:]
	}
	var wakes []core.Wake
	for _, w := range s.Wakes {
		if w.Status == core.WakeWaiting || w.Status == core.WakeFired {
			wakes = append(wakes, w)
		}
	}
	s.Wakes = wakes
	// How the assistant looks is for the owner's screen, not its turns.
	s.Assistant.Avatar, s.Assistant.AvatarSVG = config.Avatar{}, ""
	members := make([]core.Member, len(s.Members))
	for i, m := range s.Members {
		m.Avatar, m.AvatarSVG, m.Instructions = config.Avatar{}, "", text.Clip(m.Instructions, 300)
		// The newest few are enough to avoid recording one twice.
		if n := len(m.Learnings); n > 5 {
			m.Learnings = m.Learnings[n-5:]
		}
		members[i] = m
	}
	s.Members = members
	return s
}

// recentlyFinished is the ids of the last n tasks to finish.
func recentlyFinished(tasks []core.Task, n int) map[string]bool {
	var finished []core.Task
	for _, t := range tasks {
		if t.Finished() {
			finished = append(finished, t)
		}
	}
	slices.SortFunc(finished, func(a, b core.Task) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	out := map[string]bool{}
	for _, t := range finished[:min(n, len(finished))] {
		out[t.ID] = true
	}
	return out
}

// finishedBrief is a finished task as the assistant sees it each turn: what
// was asked, how it ended and where it went.
func finishedBrief(t core.Task) core.Task {
	return core.Task{ID: t.ID, ProjectID: t.ProjectID, Objective: text.Clip(t.Objective, 200), Status: t.Status, Stage: t.Stage, Detail: text.Clip(t.Detail, 200), DeliveredTo: t.DeliveredTo, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

// taskDetail is one task as read_task gives it: everything the assistant can
// use, without the team's sessions and setup.
func taskDetail(t core.Task) core.Task {
	t.WriterSession, t.Playbook, t.Roles = nil, nil, nil
	if n := len(t.Revisions); n > 3 {
		t.Revisions = t.Revisions[n-3:]
	}
	revisions := make([]core.Revision, len(t.Revisions))
	for i, r := range t.Revisions {
		r.Summary = text.Clip(r.Summary, 2000)
		revisions[i] = r
	}
	t.Revisions = revisions
	if n := len(t.Messages); n > 10 {
		t.Messages = t.Messages[n-10:]
	}
	return t
}

// clippedPlan is a plan as the assistant reads it each turn: the gist, not
// every detail.
func clippedPlan(p core.Plan) *core.Plan {
	list := func(items []string) []string {
		var out []string
		for _, item := range items[:min(len(items), 5)] {
			out = append(out, text.Clip(item, 200))
		}
		return out
	}
	p.Summary = text.Clip(p.Summary, 600)
	p.Exists, p.Changes, p.OutOfScope, p.Questions = list(p.Exists), list(p.Changes), list(p.OutOfScope), list(p.Questions)
	return &p
}
