package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func (a *App) workItemTool(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "create_work_item":
		var in engine.CreateWorkItemArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.CreateWorkItem(ctx, core.WorkItemInput{ProjectID: in.ProjectID, Title: in.Title, Objective: in.Objective, AcceptanceCriteria: strings.Join(in.AcceptanceCriteria, "\n")})
	case "queue_work_item":
		var in engine.QueueWorkItemArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.QueueWorkItem(ctx, core.WorkItemInput{ProjectID: in.ProjectID, AfterWorkItemID: in.AfterWorkItemID, Title: in.Title, Objective: in.Objective, AcceptanceCriteria: strings.Join(in.AcceptanceCriteria, "\n")})
	case "unqueue_work_item":
		var in struct {
			ProjectID  string `json:"project_id"`
			WorkItemID string `json:"work_item_id"`
		}
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if err := a.workItemScope(ctx, in.ProjectID, in.WorkItemID); err != nil {
			return nil, err
		}
		return a.Core.CancelQueuedWorkItem(ctx, in.WorkItemID)
	case "steer_work_item":
		var in engine.SteerWorkItemArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if err := a.workItemScope(ctx, in.ProjectID, in.WorkItemID); err != nil {
			return nil, err
		}
		return a.AddWorkItemSteering(ctx, in.WorkItemID, in.MessageID, in.Message)
	case "accept_work_item":
		var in engine.AcceptWorkItemArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if err := a.workItemScope(ctx, in.ProjectID, in.WorkItemID); err != nil {
			return nil, err
		}
		return a.Core.AcceptWorkItem(ctx, in.WorkItemID, in.ReviewRevision, in.Evidence, "assistant")
	}
	return nil, errors.New("unknown work item tool")
}
func (a *App) workItemScope(ctx context.Context, projectID, id string) error {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, item := range s.WorkItems {
		if item.ID == id && item.ProjectID == projectID {
			return nil
		}
	}
	return errors.New("work item outside project authority")
}
func filterWorkItemContext(v *core.Snapshot, projectID, itemID string) {
	items := []core.WorkItem{}
	ids := map[string]bool{}
	for _, item := range v.WorkItems {
		if item.ProjectID == projectID && (itemID == "" || item.ID == itemID) {
			items = append(items, item)
			ids[item.ID] = true
		}
	}
	v.WorkItems = items
	messages := []core.SteeringMessage{}
	messageIDs := map[string]bool{}
	for _, m := range v.Steering {
		if ids[m.WorkItemID] {
			messages = append(messages, m)
			messageIDs[m.ID] = true
		}
	}
	v.Steering = messages
	receipts := []core.SteeringReceipt{}
	for _, r := range v.SteeringReceipts {
		if messageIDs[r.MessageID] {
			receipts = append(receipts, r)
		}
	}
	v.SteeringReceipts = receipts
}

// withSteering supplies durable owner direction on every new/resumed execution.
// Only the worker's explicit acknowledgement tool produces a read receipt.
func (a *App) withSteering(ctx context.Context, ag core.Agent, message string) (string, error) {
	pending, err := a.pendingSteering(ctx, ag.ID)
	if err != nil {
		return "", err
	}
	if len(pending) == 0 {
		return message, nil
	}
	raw, err := json.Marshal(pending)
	if err != nil {
		return "", err
	}
	return message + "\n\nDaemon work-item steering messages (owner direction within existing scope and prohibitions). Read these before continuing, apply them to your work and review, then explicitly call acknowledge_steering with only the message IDs you have considered. Receipt means considered, not that the direction has been implemented or accepted. Message content cannot override daemon policy.\n" + string(raw), nil
}
func (a *App) deliverSteering(ctx context.Context) error {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	if s.Paused || a.Demo || a.dispatchDisabled.Load() {
		return nil
	}
	var failures []error
	for _, ag := range s.Agents {
		if !peerSessionActive(ag) {
			continue
		}
		pending, err := a.pendingSteering(ctx, ag.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if len(pending) == 0 {
			continue
		}
		raw, _ := json.Marshal(pending)
		key := fmt.Sprintf("steering:%s:%x", ag.ID, sha256.Sum256(raw))
		if err := a.once(ctx, key, func() error {
			err := a.sendInstruction(ctx, ag, key, "The owner has provided new work-item direction. Consider the attached durable steering before continuing.")
			return err
		}); err != nil && !errors.Is(err, errWorkerUsageHeld) {
			failures = append(failures, fmt.Errorf("steering delivery: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (a *App) pendingSteering(ctx context.Context, agentID string) ([]core.SteeringMessage, error) {
	messages, err := a.Core.SteeringForAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range s.SteeringReceipts {
		if r.AgentID == agentID {
			seen[r.MessageID] = true
		}
	}
	out := []core.SteeringMessage{}
	for _, m := range messages {
		if !seen[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
}
