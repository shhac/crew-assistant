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

// assistantView is the state the assistant reads each turn: an overview.
// The assistant keeps the owner's projects moving; how each request is being
// built and checked is the team's business. So a turn carries every request's
// stage and what it waits on, open decisions in full, since they are what the
// team brought up, and only the outcome of finished work. read_task and ask_pm
// bring detail when it is wanted.
func assistantView(s core.Snapshot) core.Snapshot {
	recent := recentlyFinished(s.Tasks, shownFinished)
	tasks := make([]core.Task, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		switch {
		case !t.Finished():
			tasks = append(tasks, taskOverview(t))
		case recent[t.ID]:
			tasks = append(tasks, finishedBrief(t))
		}
	}
	s.Tasks = tasks
	projects := make([]core.Project, len(s.Projects))
	for i, p := range s.Projects {
		projects[i] = projectOverview(p)
	}
	s.Projects = projects

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

	// The snapshot lists activity newest first.
	s.Activity = s.Activity[:min(len(s.Activity), 30)]
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

// taskOverview is an unfinished task as the assistant sees it each turn:
// where it is and what it waits on, not how it is being built.
func taskOverview(t core.Task) core.Task {
	return core.Task{ID: t.ID, ProjectID: t.ProjectID, Objective: text.Clip(t.Objective, 300), Status: t.Status, Stage: t.Stage, Checking: t.Checking, Answered: t.Answered, WaitsFor: t.WaitsFor, DependsOn: t.DependsOn, Detail: text.Clip(t.Detail, 200), Round: t.Round, MaxRounds: t.MaxRounds, DecisionID: t.DecisionID, DirectionPending: t.DirectionPending, RetryAt: t.RetryAt, Proposal: t.Proposal, Branch: t.Branch, CreatedAt: t.CreatedAt, StartedAt: t.StartedAt, UpdatedAt: t.UpdatedAt}
}

// projectOverview is a project without its team's standing instructions,
// which are for the team.
func projectOverview(p core.Project) core.Project {
	if p.Playbook == nil {
		return p
	}
	playbook := *p.Playbook
	playbook.Roles = make([]core.Role, len(p.Playbook.Roles))
	for i, r := range p.Playbook.Roles {
		r.Instructions, r.Learnings = "", nil
		playbook.Roles[i] = r
	}
	p.Playbook = &playbook
	return p
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
