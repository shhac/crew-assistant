package app

import (
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// assistantView is the state the assistant reads each turn: everything it can
// act on, and only the outcome of what is finished. Full revision and review
// history stays on the project pages; carrying it into every turn crowded out
// the conversation itself.
func assistantView(s core.Snapshot) core.Snapshot {
	tasks := make([]core.Task, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		t.WriterSession, t.Playbook, t.Roles = nil, nil, nil
		if t.Plan != nil {
			t.Plan = clippedPlan(*t.Plan)
		}
		keep := 2
		if t.Finished() {
			keep = 1
		}
		if n := len(t.Revisions); n > keep {
			t.Revisions = t.Revisions[n-keep:]
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
		if !t.Finished() && len(revisions) > 0 {
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
