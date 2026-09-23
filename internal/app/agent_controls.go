package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

type AgentControls struct {
	Pause   bool   `json:"pause"`
	Resume  bool   `json:"resume"`
	Stop    bool   `json:"stop"`
	Message bool   `json:"message"`
	Reason  string `json:"reason"`
}

func (a *App) AgentControls(ctx context.Context, id string) (AgentControls, error) {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return AgentControls{}, err
	}
	ag, ok := findAgent(s, id)
	if !ok {
		return AgentControls{}, core.ErrNotFound
	}
	out := AgentControls{Message: ag.Status != "completed" && ag.Status != "cancelled"}
	if a.Demo || a.dispatchDisabled.Load() {
		out.Reason = "Worker controls are disabled for this boot"
		return out, nil
	}
	has := func(action string) bool {
		for _, c := range ag.ControlCapabilities {
			if c == action {
				return true
			}
		}
		return ag.ExternalID == ""
	}
	out.Pause = has("pause") && (ag.Status == "running" || ag.Status == "waiting" || ag.Status == "blocked" || ag.Status == "queued" || ag.Status == "retry_wait" || ag.Status == "usage_wait") && ag.OwnerControl != "pause" && ag.OwnerControl != "stop"
	out.Resume = ag.OwnerControl != "stop" && has("resume") && (ag.Status == "paused" || ag.Status == "interrupted" || ag.Status == "usage_wait" || (ag.Status == "blocked" && ag.ProviderFailureKind != "")) && !s.Paused && (ag.RetryAt.IsZero() || !time.Now().Before(ag.RetryAt))
	out.Stop = has("stop") && (ag.Status == "running" || ag.Status == "waiting" || ag.Status == "blocked" || ag.Status == "queued" || ag.Status == "paused" || ag.Status == "pause_requested" || ag.Status == "interrupted" || ag.Status == "retry_wait" || ag.Status == "usage_wait") && ag.OwnerControl != "stop"
	if ag.ExternalID != "" && len(ag.ControlCapabilities) == 0 {
		out.Reason = "This broker has not advertised worker controls"
	}
	if ag.Status == "retry_wait" {
		out.Reason = "Waiting for the model provider. New direction is saved with the outcome and included in the next admitted retry; sending it does not start execution."
	}
	if ag.Status == "usage_wait" {
		out.Reason = "Waiting for worker resources. Saved work and conversation are preserved; nothing failed and no recovery attempt was used. New direction is saved with the outcome and included when work continues."
		if ag.ResourceHoldOwnerAction {
			out.Reason = "Waiting for a decision about worker resources. Change the limit in Settings, then Resume to continue this assignment with its saved context."
		}
	}
	if ag.Status == "blocked" && ag.ProviderFailureKind != "" {
		out.Reason = "The model request stopped. Inspect the saved work and correct the problem, then Resume. New direction is saved for that continuation."
	}
	if ag.Status == "pause_requested" {
		out.Reason = "Pause requested; finishing the current model or tool operation, then confirming cleanup"
	}
	if ag.Status == "stop_requested" {
		out.Reason = "Stop requested; waiting for confirmed process cleanup"
	}
	return out, nil
}
func (a *App) ControlAgent(ctx context.Context, id, action, operationID string) (core.Agent, error) {
	if a.Demo || a.dispatchDisabled.Load() {
		return core.Agent{}, errors.New("worker controls disabled for this boot")
	}
	if strings.TrimSpace(operationID) == "" || len(operationID) > 128 {
		return core.Agent{}, errors.New("bounded operation_id is required")
	}
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.Agent{}, err
	}
	ag, ok := findAgent(s, id)
	if !ok {
		return ag, core.ErrNotFound
	}
	key := "owner-control:" + id + ":" + action + ":" + operationID
	if done, exists := s.Events[key]; exists {
		if !done {
			return ag, worker.ErrUncertain
		}
		return ag, nil
	}
	controls, err := a.AgentControls(ctx, id)
	if err != nil {
		return ag, err
	}
	if !(action == "pause" && controls.Pause || action == "stop" && controls.Stop || action == "resume" && controls.Resume) {
		return ag, errors.New("worker control is not currently available")
	}
	if action == "resume" {
		if err = a.workerUsageAllowed(ctx, ag.ProfileID); err != nil {
			return ag, err
		}
	}
	var client *worker.Client
	if ag.ExternalID != "" {
		client, err = a.broker(ctx, ag.ProfileID)
		if err != nil {
			return ag, err
		}
	}
	prepared, send, err := a.Core.PrepareOwnerControl(ctx, id, action, key)
	if err != nil {
		return ag, err
	}
	if err = a.Core.RecordAgentConversation(ctx, id, key, "control", "owner_to_worker", "Owner requested "+action); err != nil {
		return prepared, errors.Join(worker.ErrUncertain, err)
	}
	if !send {
		return prepared, nil
	}
	var run worker.Run
	switch action {
	case "pause":
		run, err = client.Pause(ctx, prepared.ExternalID, key)
	case "stop":
		run, err = client.Cancel(ctx, prepared.ExternalID, key)
	case "resume":
		message, buildErr := a.withPeerRoster(ctx, prepared, "The owner explicitly resumed this preserved assignment. Continue from the saved workspace and conversation; do not repeat completed operations.")
		if buildErr != nil {
			return prepared, buildErr
		}
		run, err = client.Resume(ctx, prepared.ExternalID, key, message)
	}
	if err != nil {
		_ = a.Core.MarkUncertain(ctx, id, "Owner control outcome is uncertain; reconcile the preserved session before retrying")
		return prepared, errors.Join(worker.ErrUncertain, err)
	}
	if run.ID != prepared.ExternalID {
		return prepared, errors.Join(worker.ErrUncertain, fmt.Errorf("control returned a different worker session"))
	}
	if err = a.Core.CompleteEvent(ctx, key); err != nil {
		return prepared, errors.Join(worker.ErrUncertain, err)
	}
	if action == "resume" && run.Status != "paused" && run.Status != "interrupted" {
		if err = a.Core.ConfirmOwnerResume(ctx, id); err != nil {
			return prepared, err
		}
		prepared.OwnerControl = ""
	}
	if err = a.observeRun(ctx, prepared, run, client, false); err != nil {
		return prepared, err
	}
	current, err := a.Core.Snapshot(ctx)
	if err != nil {
		return prepared, err
	}
	ag, _ = findAgent(current, id)
	return ag, nil
}
func (a *App) SteerAgent(ctx context.Context, id, key, message string) (core.SteeringMessage, error) {
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.SteeringMessage{}, err
	}
	ag, ok := findAgent(s, id)
	if !ok {
		return core.SteeringMessage{}, core.ErrNotFound
	}
	result, err := a.AddWorkItemSteering(ctx, ag.WorkItemID, key, message)
	if err != nil {
		return result, err
	}
	err = a.Core.RecordAgentConversation(ctx, id, "owner-steering:"+key, "steering", "owner_to_worker", message)
	if err != nil {
		return result, errors.Join(worker.ErrUncertain, err)
	}
	return result, nil
}

// Delivery receipts describe broker acceptance, never model agreement or completed work.
func (a *App) recordMessageDelivery(ctx context.Context, id, key string, deliveryErr error) {
	kind, content := "delivery", "The broker accepted the preceding message. Reading and implementation are not confirmed."
	var rejected *worker.RejectionError
	if errors.As(deliveryErr, &rejected) {
		kind = "delivery_rejected"
		content = "The broker refused the preceding message before delivery. Inspect the preserved worker and use an available lifecycle control; no delivery is pending."
	} else if deliveryErr != nil {
		kind = "delivery_uncertain"
		content = "The preceding message delivery was not confirmed. The daemon will reconcile this operation before repeating it."
	}
	_ = a.Core.RecordAgentConversation(ctx, id, key+":"+kind, kind, "daemon_to_worker", content)
}
func (a *App) AddWorkItemSteering(ctx context.Context, id, key, message string) (core.SteeringMessage, error) {
	result, err := a.Core.AddSteering(ctx, id, key, message)
	if err != nil {
		return result, err
	}
	s, err := a.Core.Snapshot(ctx)
	if err != nil {
		return result, errors.Join(worker.ErrUncertain, err)
	}
	for _, ag := range s.Agents {
		if ag.WorkItemID == id && ag.Status != "completed" && ag.Status != "cancelled" {
			if err = a.Core.RecordAgentConversation(ctx, ag.ID, "owner-steering:"+key, "steering", "owner_to_worker", "Owner direction saved for this outcome; delivery and acknowledgement are tracked separately.\n"+message); err != nil {
				return result, errors.Join(worker.ErrUncertain, err)
			}
		}
	}
	return result, nil
}
