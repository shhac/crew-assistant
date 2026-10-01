package core

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// DecisionPRFlow asks the owner, where the team has no PM, whether a task
// that started with pull requests carries on with them now they are off.
const DecisionPRFlow = "pr-flow"

const (
	ChoiceKeepPR    = "Carry on with its pull request"
	ChoiceSwitchWay = "Land it the project's new way"
)

// PRFlowChoice is the PM's choice for one task: keep its pull request, or
// land the project's way now.
type PRFlowChoice struct {
	Task string
	Keep bool
}

// PRClose is a pull request to close, and what to say on it.
type PRClose struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Note   string `json:"note"`
}

// EndPullRequests is a project turning pull requests off. Its unfinished
// tasks that started with them keep them until someone chooses otherwise:
// the PM, at its next look, or the owner, asked about each, where the team
// has no PM.
func (s *Service) EndPullRequests(ctx context.Context, projectID string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if p.Playbook != nil && p.Playbook.Land.PullRequests {
			return nil
		}
		now := s.now().UTC()
		_, pm := p.PMSeat()
		for i := range v.Tasks {
			t := &v.Tasks[i]
			if t.ProjectID != projectID || t.Finished() || !t.UsesPRs() || slices.Contains(p.PRChoices, t.ID) || openPRFlowDecision(v, t.ID) {
				continue
			}
			if pm {
				p.PRChoices = append(p.PRChoices, t.ID)
				continue
			}
			context := "Pull requests are off for this project now, but this task started with them"
			if t.PROpen() {
				context += fmt.Sprintf(", and its pull request #%d is open", t.Proposal.Number)
			}
			context += ". It can carry on with them as it started, or land the project's way from here; that closes its pull request, if one is open, and asks again before it lands, as the project now does."
			v.Decisions = append(v.Decisions, Decision{ID: uid(), ProjectID: projectID, TaskID: t.ID, Kind: DecisionPRFlow, Title: fmt.Sprintf("Does “%s” carry on with its pull request?", t.Objective), Context: context, Recommendation: ChoiceKeepPR, Choices: []string{ChoiceKeepPR, ChoiceSwitchWay}, Status: DecisionOpen, CreatedAt: now})
			recordTask(v, now, t, "decision.opened", "Pull requests are off: does "+t.Objective+" carry on with its?")
		}
		if len(p.PRChoices) > 0 {
			p.PMDue = true
		}
		return nil
	})
}

func openPRFlowDecision(v *Snapshot, taskID string) bool {
	return slices.ContainsFunc(v.Decisions, func(d Decision) bool {
		return d.TaskID == taskID && d.Kind == DecisionPRFlow && d.Status == DecisionOpen
	})
}

// choosePRFlow carries out the choice for a task that started with pull
// requests: keep them, or land the project's way from here. Switching closes
// its pull request, and a change already past its checks is decided again
// the new way, since what it was approved for was a pull request.
func choosePRFlow(v *Snapshot, taskID string, keep bool, by string, now time.Time) {
	t := task(v, taskID)
	if t == nil {
		return
	}
	p := project(v, t.ProjectID)
	if p == nil {
		return
	}
	p.PRChoices = slices.DeleteFunc(p.PRChoices, func(id string) bool { return id == taskID })
	if t.Finished() || !t.UsesPRs() || p.Playbook == nil || p.Playbook.Land.PullRequests {
		return
	}
	if keep {
		recordTask(v, now, t, "task.pr_flow", fmt.Sprintf("%s kept %s on its pull request", by, t.Objective))
		return
	}
	if t.PROpen() {
		t.ClosePR = &PRClose{Repo: t.Playbook.Land.GitHub, Number: t.Proposal.Number, Note: "This change will land another way, so this pull request is closed."}
	}
	pinned := *t.Playbook
	pinned.Land = p.Playbook.Land
	t.Playbook, t.Proposal = &pinned, nil
	t.Approved, t.LandDecision = 0, nil
	cancelTaskWakes(v, t.ID)
	past := t.Status == TaskLanding || t.Status == TaskAwaiting || t.Status == TaskDeciding
	if d := decision(v, t.DecisionID); t.Status == TaskWaiting && d != nil && d.Approves() {
		if d.Status == DecisionOpen {
			dismiss(v, d, now, "Pull requests are off: it is decided again the project's way")
		}
		past = true
	}
	if past && len(t.Revisions) > 0 {
		t.Status, t.DecisionID, t.Detail = TaskDeciding, "", "Landing the project's way now"
	}
	t.UpdatedAt = now
	recordTask(v, now, t, "task.pr_flow", fmt.Sprintf("%s moved %s off pull requests, to land the project's way", by, t.Objective))
	derive(v, t)
}
