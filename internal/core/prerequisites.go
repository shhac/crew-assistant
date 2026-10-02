package core

import (
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

const (
	DecisionPrerequisite    = "prerequisite"
	ChoiceReadyPrerequisite = "It's ready"
	ChoiceDropPrerequisite  = "Don't wait for it"
)

// WaitOn is a derived pointer to unfinished work, including its project.
type WaitOn struct {
	Task      string `json:"task"`
	Ref       string `json:"ref"`
	ProjectID string `json:"project_id"`
	Project   string `json:"project"`
	Objective string `json:"objective"`
}

func waitingOn(v *Snapshot, t Task) []WaitOn {
	var out []WaitOn
	for _, id := range t.DependsOn {
		dep := unfinished(v, id)
		if dep == nil {
			continue
		}
		p := project(v, dep.ProjectID)
		if p == nil {
			continue
		}
		out = append(out, WaitOn{dep.ID, p.TaskRef(dep.Number), p.ID, p.Title, dep.Objective})
	}
	return out
}

func taskPrerequisites(t Task) []Prerequisite {
	var out []Prerequisite
	for _, b := range t.Blockers {
		if b.Kind == BlockerPrerequisite {
			out = append(out, Prerequisite{b.Description, b.ID, b.Outcome})
		}
	}
	return out
}

// The blocker and its decision are created in RecordPlan's single write.
func (s *Service) planPrerequisites(v *Snapshot, t *Task, conditions []string, now time.Time) {
	for _, what := range conditions {
		what = text.Clip(strings.TrimSpace(what), 300)
		if what == "" {
			continue
		}
		exists := false
		for _, b := range t.Blockers {
			if b.Kind == BlockerPrerequisite && sameTitle(b.Description) == sameTitle(what) {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		b := Blocker{ID: uid(), Kind: BlockerPrerequisite, Description: what, By: researcherLinker(*t), At: now}
		t.Blockers = append(t.Blockers, b)
		d := Decision{ID: uid(), Kind: DecisionPrerequisite, ProjectID: t.ProjectID, TaskID: t.ID, BlockerID: b.ID, Title: fmt.Sprintf("Is %s ready?", what), Context: fmt.Sprintf("“%s” waits for this prerequisite. Choose %q to start once it is ready, or %q to start without waiting for it. Other dependencies still apply.", t.Objective, ChoiceReadyPrerequisite, ChoiceDropPrerequisite), Choices: []string{ChoiceReadyPrerequisite, ChoiceDropPrerequisite}, Status: DecisionOpen, CreatedAt: now}
		v.Decisions = append(v.Decisions, d)
		recordTask(v, now, t, "task.blocked", what)
		recordOn(v, now, t.ProjectID, t.ID, "decision.opened", d.Title)
	}
}

func dismissPrerequisites(v *Snapshot, taskID string, now time.Time, reason string) {
	for i := range v.Decisions {
		d := &v.Decisions[i]
		if d.TaskID == taskID && d.Kind == DecisionPrerequisite && d.Status == DecisionOpen {
			dismiss(v, d, now, reason)
		}
	}
}
