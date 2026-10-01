package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// DecisionPMQuestion is a question the team's PM asked the owner about the
// order of work. The owner's answer goes to the PM's next look.
const DecisionPMQuestion = "pm-question"

// PMAnswer is what the team's PM decided about the to-do list: where each
// task in triage goes, the queued tasks in order, and the full list of what
// a task waits for, for any task it changes.
type PMAnswer struct {
	Triage  []TriageRelease
	Order   []string
	Depends map[string][]string
	Note    string
	// PRFlow is the PM's choice for tasks that started with pull requests,
	// since turned off: whether each carries on with them.
	PRFlow []PRFlowChoice
	// The order read before the PM's look.
	SeenOrderedBy string
	SeenOrderedAt time.Time
}

// TriageRelease is where the PM sends a task in triage: on to the team's
// to-do list, or to the owner with a question, keeping it in triage until
// the PM has the answer.
type TriageRelease struct {
	Task     string
	To       string
	Question string
}

// Where a task leaves triage for.
const (
	TriageToResearch = "research"
	TriageToOwner    = "owner"
)

// ApplyPM puts the PM's decision into effect and marks the list looked at.
// What each task waits for is checked like any other dependency: the same
// project, and never a loop. The order applies only to exactly the queued
// tasks. A newer owner or assistant order is preserved if it changed during
// the look. Tasks leave triage first, so the order may place those sent on.
// It reports what it changed.
func (s *Service) ApplyPM(ctx context.Context, projectID string, in PMAnswer) (string, error) {
	var triaged, changed []string
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		p.PMDue, p.PMDirection = false, ""
		now := s.now().UTC()
		for _, c := range in.PRFlow {
			id := canonicalID(v, c.Task)
			if !slices.Contains(p.PRChoices, id) {
				continue
			}
			choosePRFlow(v, id, c.Keep, "The PM", now)
		}
		// A task the PM left out is put to it again at its next look.
		p.PMDue = len(p.PRChoices) > 0
		for _, r := range in.Triage {
			if line := triage(v, p, r, now); line != "" {
				triaged = append(triaged, line)
			}
		}
		// The PM may name tasks by their readable IDs.
		depends := make(map[string][]string, len(in.Depends))
		for id, deps := range in.Depends {
			depends[canonicalID(v, id)] = canonicalIDs(v, deps)
		}
		in.Depends, in.Order = depends, canonicalIDs(v, in.Order)
		ids := make([]string, 0, len(in.Depends))
		for id := range in.Depends {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			t := unfinished(v, id)
			if t == nil || t.ProjectID != projectID {
				continue
			}
			if !pmSetsDepends(v, t, in.Depends[id], now) {
				continue
			}
			changed = append(changed, "what “"+t.Objective+"” waits for")
		}
		var orderLine string
		stale := (p.OrderedBy == OrderedByOwner || p.OrderedBy == OrderedByAssistant) &&
			(p.OrderedBy != in.SeenOrderedBy || !p.OrderedAt.Equal(in.SeenOrderedAt))
		if stale && len(in.Order) > 0 {
			who := "you"
			if p.OrderedBy == OrderedByAssistant {
				who = "the assistant"
			}
			orderLine = "Left the order as " + who + " set it while the PM was looking"
		} else if order := pmOrder(v, p, in.Order); order != nil {
			previous := orderRefs(v, queuedOf(v, projectID))
			if _, err := reorder(v, projectID, order); err == nil {
				p.OrderedBy, p.OrderedAt = OrderedByPM, now
				changed = append(changed, "the order to "+orderRefs(v, order)+" (was "+previous+")")
			}
		}
		parts := slices.Clone(triaged)
		if len(changed) > 0 {
			parts = append(parts, "Changed "+strings.Join(changed, " and "))
		}
		if orderLine != "" {
			parts = append(parts, orderLine)
		}
		summary := "The to-do list stays as it is"
		if len(parts) > 0 {
			summary = strings.Join(parts, "; ")
		}
		if note := strings.TrimSpace(in.Note); note != "" {
			summary += ": " + note
		}
		record(v, now, projectID, "task.ordered", summary)
		return nil
	})
	return strings.Join(append(triaged, changed...), ", "), err
}

// triage sends one task in triage where the PM chose, within a change, and
// says what it did, or "" when it did nothing: the task must still be in
// triage, and a question for the owner must say something.
func triage(v *Snapshot, p *Project, r TriageRelease, now time.Time) string {
	t := task(v, strings.TrimSpace(r.Task))
	if t == nil || t.ProjectID != p.ID || t.Status != TaskTriage {
		return ""
	}
	switch strings.TrimSpace(r.To) {
	case TriageToResearch:
		release(v, t, now)
		recordTask(v, now, t, "task.triaged", "Sent on to the team: "+t.Objective)
		return "Sent “" + t.Objective + "” on to the team"
	case TriageToOwner:
		question := text.Clip(strings.TrimSpace(r.Question), 2000)
		if question == "" {
			return ""
		}
		if d := decision(v, t.DecisionID); d != nil && d.Status == DecisionOpen {
			return ""
		}
		pm := "The PM"
		if seat, ok := p.PMSeat(); ok {
			pm = seat.Name
		}
		d := Decision{ID: uid(), Kind: DecisionPMQuestion, TaskID: t.ID, ProjectID: p.ID, Title: fmt.Sprintf("%s asks about “%s” before it starts", pm, text.Clip(t.Objective, 120)), Context: question, Recommendation: "Answer, or let the PM use its judgment", Choices: []string{"Use your judgment", "Send it on as it is"}, Status: DecisionOpen, CreatedAt: now}
		v.Decisions = append(v.Decisions, d)
		t.DecisionID, t.UpdatedAt = d.ID, now
		recordTask(v, now, t, "decision.opened", d.Title)
		derive(v, t)
		return "Asked you about “" + t.Objective + "”"
	}
	return ""
}

// release moves a task out of triage onto the to-do list, where it starts
// with the researcher, closing any question it still waits on.
func release(v *Snapshot, t *Task, now time.Time) {
	if d := decision(v, t.DecisionID); d != nil && d.Status == DecisionOpen {
		dismiss(v, d, now, "No longer needed: the task went on to the team")
	}
	t.Status, t.DecisionID, t.UpdatedAt = TaskQueued, "", now
	derive(v, t)
}

// ReleaseTriage sends every task in a project's triage on to its to-do
// list, when there is no PM to shape them or its PM could not look, so
// nothing waits on a missing role. It says why in one line.
func (s *Service) ReleaseTriage(ctx context.Context, projectID, why string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if project(v, projectID) == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		released := 0
		for i := range v.Tasks {
			if t := &v.Tasks[i]; t.ProjectID == projectID && t.Status == TaskTriage {
				release(v, t, now)
				released++
			}
		}
		if released > 0 {
			record(v, now, projectID, "task.triaged", fmt.Sprintf("Sent on to the team without triage (%d): %s", released, why))
		}
		return nil
	})
}

// HasTriage says whether any of a project's tasks wait in triage.
func (v Snapshot) HasTriage(projectID string) bool {
	return slices.ContainsFunc(v.Tasks, func(t Task) bool { return t.ProjectID == projectID && t.Status == TaskTriage })
}

// pmOrder is the order the PM may set, or nil to leave it. It must name
// exactly the queued tasks and change something.
func pmOrder(v *Snapshot, p *Project, order []string) []string {
	current := queuedOf(v, p.ID)
	if !sameTasks(order, current) || slices.Equal(order, current) {
		return nil
	}
	return order
}

// orderRefs names an order as the owner sees it, with canonical IDs as fallback.
func orderRefs(v *Snapshot, order []string) string {
	refs := make([]string, 0, len(order))
	for _, id := range order {
		t := task(v, id)
		ref := t.Ref
		if ref == "" {
			ref = id
		}
		refs = append(refs, ref)
	}
	return strings.Join(refs, ", ")
}

// queuedOf is a project's queued tasks, in the order they start in.
func queuedOf(v *Snapshot, projectID string) []string {
	var out []string
	for _, t := range v.Tasks {
		if t.ProjectID == projectID && t.Status == TaskQueued {
			out = append(out, t.ID)
		}
	}
	return out
}

// sameTasks reports whether two lists name the same tasks, each once.
func sameTasks(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b) && len(slices.Compact(a)) == len(b)
}

// AskForPM brings the PM's questions about a project's order of work to the
// owner. The answer goes to the PM's next look.
func (s *Service) AskForPM(ctx context.Context, projectID string, in DecisionInput) (Decision, error) {
	in.ProjectID = projectID
	return s.openDecision(ctx, DecisionPMQuestion, in)
}

// PMSeat is the seat on a project's team that keeps its to-do list.
func (p Project) PMSeat() (Role, bool) {
	if p.Playbook == nil {
		return Role{}, false
	}
	i := slices.IndexFunc(p.Playbook.Roles, func(r Role) bool { return r.Holds(RolePM) })
	if i < 0 {
		return Role{}, false
	}
	return p.Playbook.Roles[i], true
}

// listChanged asks the project's PM, if it has one, to look at the to-do
// list again.
func (p *Project) listChanged() {
	if _, ok := p.PMSeat(); ok {
		p.PMDue = true
	}
}

// SkipPM marks the list looked at without the PM, when the project has no
// PM or its PM could not look, saying why.
func (s *Service) SkipPM(ctx context.Context, projectID, why string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		p.PMDue = false
		if why != "" {
			record(v, s.now().UTC(), projectID, "task.ordered", fmt.Sprintf("The PM couldn't look at the to-do list: %s", why))
		}
		return nil
	})
}
