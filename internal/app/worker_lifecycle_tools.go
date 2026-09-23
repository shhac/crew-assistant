package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

// ownerChatExecutor carries authority from the current owner turn. Background
// review, queue and supervision executors cannot synthesize this authority.
type ownerChatExecutor struct {
	app    *App
	turnID string
}

func (s ownerChatExecutor) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	if name != "control_agent" {
		return s.app.Execute(ctx, name, raw)
	}
	if s.turnID == "" {
		return nil, errors.New("worker control requires a current owner turn")
	}
	var in engine.ControlAgentArgs
	if err := args(raw, &in); err != nil {
		return nil, err
	}
	// One durable operation per action/assignment in this turn, regardless of
	// retries or model-generated tool IDs. A new owner turn is a new request.
	return s.app.ControlAgent(ctx, in.AgentID, in.Action, "chat:"+s.turnID)
}

type AgentInspection struct {
	Agent        core.Agent                 `json:"agent"`
	Controls     AgentControls              `json:"controls"`
	Resources    AgentResources             `json:"resources"`
	Conversation core.AgentConversationPage `json:"conversation"`
}

// AgentResources states an assignment's consumption in one place so it is not
// reassembled from separate fields, and so unmeasured calls are visible rather
// than silently counted as nothing.
type AgentResources struct {
	UsedTokens       int64  `json:"used_tokens"`
	UnknownCalls     int    `json:"calls_with_unknown_usage"`
	TokenBudget      int64  `json:"token_budget"`
	BudgetConfigured bool   `json:"token_budget_configured"`
	WaitingOn        string `json:"waiting_on,omitempty"`
	Explanation      string `json:"explanation"`
}

func agentResources(agent core.Agent) AgentResources {
	out := AgentResources{UsedTokens: agent.UsageInputTokens + agent.UsageOutputTokens, UnknownCalls: agent.UsageUnknownCalls, TokenBudget: agent.TokenBudget, BudgetConfigured: agent.TokenBudget > 0}
	out.Explanation = "Input tokens include cached input, as the provider reports it. Worker limits are resource limits: shared subscription headroom, and an optional per-assignment token budget. There is no limit on turns, tools or how long an assignment may run."
	if out.UnknownCalls > 0 {
		out.Explanation += " Some calls reported no usage, so used_tokens is a lower bound, not the total."
	}
	if agent.Status == "usage_wait" {
		switch agent.ResourceHoldKind {
		case worker.HoldSubscriptionQuota:
			out.WaitingOn = "subscription_headroom"
		case worker.HoldTelemetryUnavailable:
			out.WaitingOn = "subscription_usage_unreadable"
		case worker.HoldTokenBudget:
			out.WaitingOn = "token_budget"
		case worker.HoldUsageUnknown:
			out.WaitingOn = "unestablished_usage"
		default:
			out.WaitingOn = "worker_resources"
		}
	}
	return out
}

func (a *App) InspectAgent(ctx context.Context, id string) (AgentInspection, error) {
	snapshot, err := a.Core.Snapshot(ctx)
	if err != nil {
		return AgentInspection{}, err
	}
	agent, ok := findAgent(snapshot, id)
	if !ok {
		return AgentInspection{}, core.ErrNotFound
	}
	controls, err := a.AgentControls(ctx, id)
	if err != nil {
		return AgentInspection{}, err
	}
	conversation, err := a.Core.AgentConversation(ctx, id, 0, 0, 20)
	if err != nil {
		return AgentInspection{}, err
	}
	return AgentInspection{Agent: agent, Controls: controls, Resources: agentResources(agent), Conversation: conversation}, nil
}

// An admission reservation is not evidence the worker is running. A refusal
// usually means our last snapshot raced a worker transition. Refresh read-only;
// inability to inspect the worker is distinct from uncertain message delivery.
func (a *App) refreshRefusedInstruction(ctx context.Context, agent core.Agent, client *worker.Client) {
	run, err := client.Get(ctx, agent.ExternalID)
	if err == nil {
		err = a.observeRun(ctx, agent, run, client, false)
	}
	if err != nil {
		_ = a.Core.MarkUncertain(ctx, agent.ID, "Message was refused before delivery; the worker's current state could not be refreshed. Inspect the preserved session before further execution.")
	}
}
