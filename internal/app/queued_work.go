package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

type reasoningScope interface {
	engine.ToolExecutor
	context(context.Context) (json.RawMessage, error)
}

// A queued outcome authorizes commissioning only that recorded contract. Worker
// reports cannot manufacture this scope or request further owner work.
type queuedWorkExecutor struct{ projectExecutor }

func (s queuedWorkExecutor) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "prepare_worker":
		var in struct {
			ProjectID string `json:"project_id"`
			Workspace string `json:"workspace"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if in.ProjectID != s.projectID {
			return nil, errors.New("setup outside queued outcome")
		}
		ready, err := s.app.Core.ReadyQueuedWorkItems(ctx)
		if err != nil {
			return nil, err
		}
		allowed := false
		for _, item := range ready {
			if item.ID == s.workItemID {
				allowed = true
			}
		}
		if !allowed {
			return nil, errors.New("queued outcome is no longer ready for commissioning")
		}
		return s.app.Execute(ctx, name, raw)
	case "delegate":
		var in engine.DelegateArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if in.ProjectID != s.projectID || in.WorkItemID != s.workItemID || in.ParentID != "" {
			return nil, errors.New("delegation outside queued outcome")
		}
		return s.app.commissionWorker(ctx, core.DelegateInput{ProjectID: in.ProjectID, WorkItemID: in.WorkItemID, ProfileID: in.WorkerProfile, Role: in.Role, Task: in.Objective, AcceptanceCriteria: strings.Join(in.AcceptanceCriteria, "\n"), RequireCommissionRequest: true})
	case "read_state", "ask_decision", "report_status":
		return s.projectExecutor.Execute(ctx, name, raw)
	default:
		return nil, errors.New("action unavailable when commissioning queued work")
	}
}

func (a *App) commissionQueuedWork(ctx context.Context) error {
	if a.Demo || a.dispatchDisabled.Load() {
		return nil
	}
	ready, err := a.Core.ReadyQueuedWorkItems(ctx)
	if err != nil {
		return err
	}
	for _, item := range ready {
		model := a.Config().Model
		key := fmt.Sprintf("queued-work:%s:%s:%s/%s/%s", item.ID, item.ReviewRevision, model.Engine, model.Model, model.Effort)
		if err := a.once(ctx, key, func() error {
			if !modelAvailable(model) {
				return a.holdQueuedWork(ctx, item, "Assistant setup is required", "The preceding outcome is accepted, but the configured assistant model is unavailable. Check the model and local CLI setup in Settings before retrying. No worker was started.")
			}
			scope := queuedWorkExecutor{projectExecutor{app: a, projectID: item.ProjectID, workItemID: item.ID}}
			result, err := a.reasoning(ctx, scope, "The owner explicitly queued this recorded outcome to start after its predecessor was accepted. The prerequisite is satisfied. Read the scoped contract and approved profiles; prepare a worker if needed, then commission the narrowest useful assignments for this exact work_item_id. The saved objective and criteria are the authorized scope. Do not ask for the same permission again, invent additional work, or claim a start without a delegate result. If a genuine unresolved choice blocks commissioning, ask a prepared decision for this work item. You cannot accept work in this turn.")
			if err != nil {
				return err
			}
			needsAction, err := a.queuedWorkNeedsAction(ctx, item.ID)
			if err != nil {
				return err
			}
			if needsAction {
				return a.holdQueuedWork(ctx, item, "Commissioning did not start", "The assistant finished its coordination turn without creating an execution assignment or a blocking decision. The outcome is still unstarted; a conversational reply is not evidence of commissioning.")
			}
			return a.Core.RecordActivity(ctx, item.ProjectID, "work_item.queue_coordinated", result.Message)
		}); err != nil {
			return err
		}
	}
	return nil
}

// A completed model turn is not a completed commissioning operation. Durable
// effects decide whether the request progressed, including a concurrent owner
// withdrawal or explicit delegation outside this turn.
func (a *App) queuedWorkNeedsAction(ctx context.Context, id string) (bool, error) {
	v, err := a.Core.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	var item *core.WorkItem
	for i := range v.WorkItems {
		if v.WorkItems[i].ID == id {
			item = &v.WorkItems[i]
			break
		}
	}
	if item == nil || !item.CommissionRequested {
		return false, nil
	}
	for _, agent := range v.Agents {
		if agent.WorkItemID == id {
			return false, nil
		}
	}
	for _, decision := range v.Decisions {
		if decision.Status == "open" && (decision.WorkItemID == id || (decision.WorkItemID == "" && (decision.ProjectID == "" || decision.ProjectID == item.ProjectID))) {
			return false, nil
		}
	}
	return true, nil
}

// One visible decision bounds a failed commissioning attempt. Resolving it
// changes the work item's revision and authorizes another coordination attempt;
// polling an unchanged blocked queue cannot repeatedly spend model allowance.
func (a *App) holdQueuedWork(ctx context.Context, item core.WorkItem, title, reason string) error {
	needsAction, err := a.queuedWorkNeedsAction(ctx, item.ID)
	if err != nil || !needsAction {
		return err
	}
	_, err = a.Core.CreateDecision(ctx, core.DecisionInput{
		ProjectID: item.ProjectID, WorkItemID: item.ID,
		Title:          title + ": " + item.Title,
		Context:        reason + " The recorded outcome and original commissioning request are preserved. Resolve this decision to try commissioning once more. To withdraw the request instead, remove the outcome from the queue before resolving it.",
		Recommendation: "Check the assistant setup and retry the saved outcome. No new scope or permission is needed.",
		Choices:        []string{"Retry using the current setup", "Setup corrected; retry commissioning"},
	})
	return err
}
