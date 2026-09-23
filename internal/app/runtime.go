package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	linearapi "github.com/shhac/crew-assistant/internal/integrations/linear"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

// Run owns deterministic supervision. noDispatch is fixed at process boot;
// changing pause or live configuration cannot enable starts or resumes beneath it.
func (a *App) Run(ctx context.Context, noDispatch bool) error {
	defer a.closeManagedWorkers()
	if noDispatch {
		a.SetNoDispatch()
	}
	ctx, cancel := context.WithCancel(ctx)
	var listeners sync.WaitGroup
	defer func() { cancel(); listeners.Wait() }()
	if a.Demo {
		<-ctx.Done()
		return nil
	}
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		if err := a.RunChatQueue(ctx); err != nil && ctx.Err() == nil {
			a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "chat_queue"}, err)
			a.Status("chat", "Conversation", "error", "The message queue stopped; restart the daemon to recover pending messages")
		}
	}()
	pending, err := a.Core.PendingEvents(ctx)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		a.Core.RecordActivity(ctx, "", "recovery.pending", fmt.Sprintf("%d interrupted inbound or outbound operations need inspection. Uncertain effects were not replayed.", len(pending)))
	}
	var slackClient *slackapi.Client
	cfg := a.Config()
	if cfg.Slack.OwnerUserID != "" {
		slackClient, err = slackapi.New(slackapi.Config{BotTokenEnv: cfg.Slack.BotTokenEnv, AppTokenEnv: cfg.Slack.AppTokenEnv, OwnerUserID: cfg.Slack.OwnerUserID}, inbox{a.Core})
		if err != nil {
			a.Status("slack", "Slack bot messaging", "error", err.Error())
		} else {
			a.Status("slack", "Slack bot messaging", "configured", "Owner DM listener starting")
			listeners.Add(1)
			go func() {
				defer listeners.Done()
				err := slackClient.Run(ctx, func(c context.Context, m slackapi.Message) (string, error) {
					result, chatErr := a.Chat(c, m.Text)
					if chatErr != nil {
						return "", chatErr
					}
					if e := a.Core.CompleteEvent(c, "slack:"+m.ID); e != nil {
						return "", e
					}
					return result.Message, nil
				})
				if ctx.Err() == nil && err != nil {
					a.Status("slack", "Slack bot messaging", "error", err.Error())
				}
			}()
		}
	}
	_ = a.SyncLinear(ctx)
	supervise := func() {
		if err := a.tick(ctx, noDispatch); err != nil && ctx.Err() == nil {
			a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "worker_supervision"}, err)
			a.Status("workers", "Worker runtimes", "error", err.Error())
		}
		if slackClient != nil {
			a.notify(ctx, slackClient.Notify)
		}
	}
	supervise()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	linearTick := time.NewTicker(5 * time.Minute)
	defer linearTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			supervise()
		case <-linearTick.C:
			_ = a.SyncLinear(ctx)
		}
	}
}

type inbox struct{ s *core.Service }

func (i inbox) Claim(ctx context.Context, id string) (bool, error) {
	return i.s.ClaimEvent(ctx, "slack:"+id)
}

func (a *App) SyncLinear(ctx context.Context) error {
	if a.Demo {
		return nil
	}
	cfg := a.Config()
	cliErr := a.syncCLIConnections(ctx)
	if !cfg.LegacyLinearImportEnabled() {
		return cliErr
	}
	c, err := linearapi.New(linearapi.Config{APIKeyEnv: cfg.Linear.APIKeyEnv, TeamIDs: cfg.Linear.TeamIDs})
	if err == nil {
		err = a.syncLinear(ctx, c)
	}
	if err != nil {
		a.Status("linear", "Linear", "error", err.Error())
	}
	return errors.Join(cliErr, err)
}

type assignmentSource interface {
	Assigned(context.Context) (linearapi.Assignments, error)
}

func (a *App) syncLinear(ctx context.Context, c assignmentSource) error {
	result, err := c.Assigned(ctx)
	if err != nil {
		return err
	}
	for _, issue := range result.Issues {
		description := issue.Description + "\nSource: " + issue.URL + "\nLinear status: " + issue.State.Name
		_, err = a.Core.CreateProject(ctx, core.ProjectInput{Title: issue.Identifier + " · " + issue.Title, Description: description, SourceID: "linear:" + issue.ID, AcceptanceCriteria: "Deliver the source outcome: " + issue.Title + ". Define measurable acceptance checks from the source requirements before commissioning work. Source: " + issue.URL})
		if err != nil {
			return err
		}
	}
	a.Status("linear", "Linear", "connected", fmt.Sprintf("%d scoped assignments synced; no work starts without a commission", len(result.Issues)))
	return nil
}
func (a *App) broker(ctx context.Context, profileID string) (*worker.Client, error) {
	p, err := a.Core.GetProfile(profileID)
	if err != nil {
		return nil, err
	}
	if p.Managed {
		manager, err := a.managedWorkers()
		if err != nil {
			return nil, err
		}
		return manager.Client(ctx, p.ProjectID, p.Workspace, workerModel(a.Config(), p))
	}
	return worker.New(worker.Config{Endpoint: p.Endpoint, APIKeyEnv: p.APIKeyEnv, Capabilities: p.Capabilities})
}
func (a *App) tick(ctx context.Context, noDispatch bool) error {
	if a.Demo {
		return nil
	}
	if err := a.Core.CheckIn(ctx); err != nil {
		return err
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, agent := range snap.Agents {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if agent.Status == "completed" || agent.Status == "cancelled" {
			if !noDispatch && !snap.Paused && agent.ParentID != "" {
				if err := a.forwardProgress(ctx, agent, worker.Run{ID: agent.ExternalID, Status: agent.Status, Summary: agent.Summary, Evidence: agent.Evidence, UpdatedAt: agent.BrokerUpdatedAt}); err != nil && !errors.Is(err, errWorkerUsageHeld) {
					failures = append(failures, err)
				}
			}
			continue
		}
		if err := a.superviseAgent(ctx, agent, noDispatch || snap.Paused); err != nil && !errors.Is(err, errWorkerUsageHeld) {
			failures = append(failures, fmt.Errorf("%s: %w", agent.Name, err))
		}
	}
	if !noDispatch && !snap.Paused {
		if err = a.deliverSteering(ctx); err != nil {
			failures = append(failures, err)
		}
		if err = a.propagateDecisions(ctx); err != nil && !errors.Is(err, errWorkerUsageHeld) {
			failures = append(failures, err)
		}
		if err = a.reviewFinished(ctx); err != nil {
			failures = append(failures, err)
		}
		if err = a.commissionQueuedWork(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if len(a.Config().Workers) > 0 {
		a.Status("workers", "Worker runtimes", "connected", "Supervision active; uncertain operations are reconciled before recovery")
	}
	return nil
}
func (a *App) superviseAgent(ctx context.Context, agent core.Agent, noDispatch bool) error {
	if agent.ExternalID == "" && agent.Status == "paused" {
		return nil
	}
	if agent.Status == "queued" && (noDispatch || agent.OwnerControl == "pause" || agent.OwnerControl == "stop") {
		return nil
	}
	if agent.Status == "queued" {
		if err := a.workerUsageAllowed(ctx, agent.ProfileID); err != nil {
			if errors.Is(err, errWorkerUsageHeld) {
				return nil
			}
			return err
		}
	}
	c, err := a.broker(ctx, agent.ProfileID)
	if err != nil {
		return err
	}
	if agent.Status == "queued" {
		// Validate credentials before recording intent; missing local setup is not an
		// ambiguous external effect and must not strand a launch in reconciliation.
		profile, _ := a.Core.GetProfile(agent.ProfileID)
		if profile.APIKeyEnv != "" && os.Getenv(profile.APIKeyEnv) == "" {
			return fmt.Errorf("worker credential environment variable %s is not set", profile.APIKeyEnv)
		}
		agent, err = a.Core.BeginDispatch(ctx, agent.ID)
		if err != nil {
			return err
		}
		role := agent.Role
		if role == "reviewer" || role == "researcher" {
			role = "worker"
		}
		task, rosterErr := a.withPeerRoster(ctx, agent, agent.Task)
		if rosterErr != nil {
			return rosterErr
		}
		if err = a.Core.RecordAgentConversation(ctx, agent.ID, "assignment:"+agent.DispatchKey, "assignment", "daemon_to_worker", agent.Task+"\nAcceptance criteria: "+agent.AcceptanceCriteria); err != nil {
			return err
		}
		run, startErr := c.Start(ctx, worker.StartRequest{DispatchKey: agent.DispatchKey, AgentID: agent.ID, ProjectID: agent.ProjectID, ParentID: agent.ParentID, Role: role, Task: task, AcceptanceCriteria: agent.AcceptanceCriteria, Capabilities: agent.Capabilities, CheckInDeadline: agent.NextCheckIn})
		if startErr != nil {
			a.Core.MarkUncertain(ctx, agent.ID, "Start was not acknowledged; reconcile by the saved dispatch key before repeating")
			return startErr
		}
		if err = a.Core.MarkDispatched(ctx, agent.ID, run.ID); err != nil {
			return err
		}
		agent.ExternalID = run.ID
		agent.Status = "running"
		return a.observeRun(ctx, agent, run, c, !noDispatch)
	}
	// Every saved dispatching/resuming intent is read back first, including after
	// restart. A missing run is uncertainty, never permission to call Start again.
	var run worker.Run
	if agent.ExternalID != "" {
		run, err = c.Get(ctx, agent.ExternalID)
	} else {
		run, err = c.Find(ctx, agent.DispatchKey)
	}
	if err != nil {
		reason := "Worker status unavailable; preserving session and checking again"
		if errors.Is(err, worker.ErrNotFound) {
			reason = "Broker has no matching run; inspect the saved dispatch intent before recovery"
		}
		a.Core.MarkUncertain(ctx, agent.ID, reason)
		return err
	}
	if agent.ExternalID == "" {
		if err = a.Core.MarkDispatched(ctx, agent.ID, run.ID); err != nil {
			return err
		}
		agent.ExternalID = run.ID
	}
	if err = a.observeRun(ctx, agent, run, c, !noDispatch); err != nil {
		return err
	}
	// Only a confirmed interrupted result may resume. A saved resuming intent
	// whose response was lost is never repeated, even if the broker still says
	// interrupted; its operation receipt requires inspection.
	if continuable(run) && !noDispatch && agent.OwnerControl != "pause" && agent.OwnerControl != "stop" && agent.Status != "resuming" && !(agent.Status == "reconciling" && agent.ResumeKey != "") {
		if err := a.workerUsageAllowed(ctx, agent.ProfileID); err != nil {
			if errors.Is(err, errWorkerUsageHeld) {
				return nil
			}
			return err
		}
		prepared, resumeErr := a.Core.PrepareResume(ctx, agent.ID)
		if resumeErr != nil {
			return resumeErr
		}
		resumeMessage, rosterErr := a.withPeerRoster(ctx, prepared, "Resume this existing session with its saved context. Reconcile existing assignments before commissioning additional work; preserve the original acceptance criteria and prohibitions.")
		if rosterErr != nil {
			return rosterErr
		}
		resumed, resumeErr := c.Resume(ctx, prepared.ExternalID, prepared.ResumeKey, resumeMessage)
		if resumeErr != nil {
			a.Core.MarkUncertain(ctx, agent.ID, "Resume outcome is uncertain; reconcile the existing session before further action")
			return resumeErr
		}
		if resumed.ID != prepared.ExternalID {
			a.Core.MarkUncertain(ctx, agent.ID, "Resume returned a different session; operator inspection required")
			return errors.New("resume returned a different external session")
		}
		if err = a.Core.MarkDispatched(ctx, agent.ID, resumed.ID); err != nil {
			return err
		}
		prepared.Status = "running"
		return a.observeRun(ctx, prepared, resumed, c, !noDispatch)
	}
	return nil
}

// agentUpdate restates a broker report in the daemon's own vocabulary. The
// resource ledger travels with every report so the owner sees what an
// assignment has consumed, not only what it is waiting for.
func agentUpdate(run worker.Run, status string) core.AgentUpdate {
	out := core.AgentUpdate{ModelFailureEngine: run.ModelFailureEngine, ModelFailurePhase: run.ModelFailurePhase, ModelFailureCode: run.ModelFailureCode, ModelFailureEvidence: run.ModelFailureEvidence, ModelExitCode: run.ModelExitCode, ContextCompactions: run.ContextCompactions, ContextUsedPercent: run.Context.UsedPercent, ContextQuality: run.Context.Quality, RetryAt: run.RetryAt, ProviderFailures: run.ProviderFailures, ProviderFailureKind: run.ProviderFailureKind, Status: status, Summary: run.Summary, Evidence: run.Evidence, ExternalID: run.ID, UpdatedAt: run.UpdatedAt,
		UsageInputTokens: run.Usage.InputTokens, UsageOutputTokens: run.Usage.OutputTokens, UsageUnknownCalls: run.Usage.UnknownCalls, TokenBudget: run.Usage.TokenBudget,
		ObservedInputTokens: run.Usage.ObservedInputTokens, ObservedOutputTokens: run.Usage.ObservedOutputTokens}
	if run.Session != nil {
		out.SessionEngine, out.SessionResumed = run.Session.Engine, run.Session.Resumed
	}
	if run.ResourceHold != nil {
		out.ResourceHoldKind, out.ResourceHoldOwnerAction, out.ResourceHoldResetsAt = run.ResourceHold.Kind, run.ResourceHold.OwnerAction, run.ResourceHold.ResetsAt
	}
	out.Work = workObservations(run.Activity)
	return out
}

// workObservations carries the worker's recent activity through to the owner's
// surfaces. A native worker can spend an hour inside one turn, so a single
// summary line is not enough to tell working from stuck; this is what makes the
// difference visible. The daemon has already bounded and sanitized it.
func workObservations(entries []worker.Activity) []core.AgentWork {
	// Only the recent tail travels. The full record stays with the assignment,
	// where it can be inspected without putting it in every status report.
	const shown = 40
	dropped := false
	if len(entries) > shown {
		entries, dropped = entries[len(entries)-shown:], true
	}
	out := make([]core.AgentWork, 0, len(entries))
	for _, entry := range entries {
		out = append(out, core.AgentWork{At: entry.At, Kind: entry.Kind, Tool: entry.Tool, Status: entry.Status, Detail: entry.Detail, Truncated: entry.Truncated})
	}
	if dropped && len(out) > 0 {
		// Say the feed starts mid-assignment. Without this it reads as the whole
		// of what the worker did.
		out[0].Truncated = true
	}
	return out
}

// continuable reports whether the daemon may continue this session by itself.
// A resource wait that clears on its own qualifies; one that needs the owner to
// change a budget, or to decide about consumption that could not be measured,
// does not. Owner pause and stop, a global pause and no-dispatch are checked
// separately and always win.
func continuable(run worker.Run) bool {
	if run.Status == "interrupted" {
		return true
	}
	if run.Status == "retry_wait" {
		return !run.RetryAt.IsZero() && !time.Now().Before(run.RetryAt)
	}
	if run.Status == "usage_wait" {
		// A report with no hold, an unrecognized kind, or no stated next check
		// is not a schedule this daemon may invent one for. Those wait for the
		// owner, who can resume by hand at any point.
		hold := run.ResourceHold
		if hold == nil || !hold.Recheckable() || hold.NextCheckAt.IsZero() {
			return false
		}
		// The broker's own next check is what governs. A published reset is when
		// the provider expects its window to roll over, not the earliest moment
		// work can continue: the owner may raise or disable the threshold, and
		// admission re-reads both policy and account before anything restarts.
		return !time.Now().Before(hold.NextCheckAt)
	}
	return false
}

func (a *App) observeRun(ctx context.Context, agent core.Agent, run worker.Run, c *worker.Client, allowActions bool) error {
	if run.UpdatedAt.IsZero() || run.UpdatedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("broker report needs a valid updated_at timestamp")
	}
	pendingResume := agent.ResumeKey != "" && (agent.Status == "resuming" || agent.Status == "reconciling")
	unchangedProviderWait := run.Status == "retry_wait" && run.ProviderFailures == agent.ProviderFailures && run.RetryAt.Equal(agent.RetryAt)
	// A resource wait carries no attempt counter, so the report timestamp is what
	// distinguishes a hold the resume never reached from one it ran into again.
	unchangedResourceWait := run.Status == "usage_wait" && !agent.BrokerUpdatedAt.IsZero() && run.UpdatedAt.Equal(agent.BrokerUpdatedAt)
	if pendingResume && (run.Status == "interrupted" || unchangedProviderWait || unchangedResourceWait) {
		return a.Core.MarkUncertain(ctx, agent.ID, "Resume acknowledgement is unresolved; the broker still reports the preceding interruption or wait. Inspect this operation before another resume.")
	}
	if agent.ExternalID != "" && run.ID != agent.ExternalID {
		return errors.New("worker update identity does not match")
	}
	if pendingResume && ((run.Status == "retry_wait" && !unchangedProviderWait) || (run.Status == "usage_wait" && !unchangedResourceWait)) {
		// A fresh rejection or hold proves the previous resume progressed. Clear
		// its intent before admitting a later, independently scheduled attempt.
		if !agent.BrokerUpdatedAt.IsZero() && run.UpdatedAt.Before(agent.BrokerUpdatedAt) {
			return errors.New("worker report is older than the recorded report")
		}
		if err := a.Core.MarkDispatched(ctx, agent.ID, run.ID); err != nil {
			return err
		}
		agent.ResumeKey = ""
		agent.Status = "running"
	}
	if len(run.SteeringAcknowledgements) > 0 {
		if err := a.Core.AcknowledgeSteering(ctx, agent.ID, run.SteeringAcknowledgements); err != nil {
			return err
		}
	}
	if agent.ControlKey != "" && ((agent.OwnerControl == "pause" && (run.Status == "paused" || run.Status == "completed" || run.Status == "cancelled")) || (agent.OwnerControl == "stop" && (run.Status == "cancelled" || run.Status == "completed")) || (agent.OwnerControl == "resume" && (run.Status == "running" || run.Status == "queued" || run.Status == "waiting"))) {
		if err := a.Core.CompleteEvent(ctx, agent.ControlKey); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(agent.ControlCapabilities, run.ControlCapabilities) {
		if err := a.Core.SetAgentControlCapabilities(ctx, agent.ID, run.ControlCapabilities); err != nil {
			return err
		}
	}
	if agent.OwnerControl == "resume" && (run.Status == "running" || run.Status == "queued" || run.Status == "waiting") {
		if err := a.Core.ConfirmOwnerResume(ctx, agent.ID); err != nil {
			return err
		}
		agent.OwnerControl = ""
	}
	status := run.Status
	if status == "queued" {
		status = "running"
	}
	if run.PauseRequested || (agent.OwnerControl == "pause" && (status == "running" || status == "queued")) {
		status = "pause_requested"
	}
	if run.StopRequested || (agent.OwnerControl == "stop" && (status == "running" || status == "queued")) {
		status = "stop_requested"
	}
	if status == "failed" {
		status = "blocked"
	}
	if strings.TrimSpace(run.Summary) == "" {
		return errors.New("broker report requires a substantive summary")
	}
	stale := time.Since(run.UpdatedAt) > time.Duration(a.Config().Limits.CheckInMinutes)*time.Minute
	if stale && (status == "running" || status == "waiting") {
		if !run.UpdatedAt.Equal(agent.BrokerUpdatedAt) {
			if _, err := a.Core.UpdateAgent(ctx, agent.ID, agentUpdate(run, status)); err != nil {
				return err
			}
		}
		if err := a.Core.MarkUncertain(ctx, agent.ID, "Worker report is stale; its process may be alive without making progress"); err != nil {
			return err
		}
	} else if !run.UpdatedAt.Equal(agent.BrokerUpdatedAt) || status != agent.Status || run.Summary != agent.Summary || !reflect.DeepEqual(run.Evidence, agent.Evidence) {
		if _, err := a.Core.UpdateAgent(ctx, agent.ID, agentUpdate(run, status)); err != nil {
			return err
		}
	}
	report := run.Summary
	if run.Message != nil {
		report += "\nPeer message: " + run.Message.Message
	}
	if run.Instruction != nil {
		report += "\nCoordination instruction: " + run.Instruction.Message
	}
	if run.Decision != nil {
		report += "\nQuestion: " + run.Decision.Question + "\nRecommendation: " + run.Decision.Recommendation
	}
	digest := sha256.Sum256([]byte(status + "\n" + report))
	if err := a.Core.RecordAgentConversation(ctx, agent.ID, fmt.Sprintf("report:%x", digest), "report", "worker_to_daemon", report); err != nil {
		return err
	}
	if status == "retry_wait" || status == "usage_wait" || !allowActions || agent.OwnerControl == "pause" || agent.OwnerControl == "stop" || status == "paused" || status == "pause_requested" || status == "stop_requested" {
		return nil
	}
	if err := a.checkProgress(ctx, agent, run); err != nil {
		return err
	}
	if agent.ParentID != "" {
		if err := a.forwardProgress(ctx, agent, run); err != nil {
			return err
		}
	}
	if run.Decision != nil {
		if err := a.routeQuestion(ctx, agent, *run.Decision); err != nil {
			return err
		}
	}
	if run.Delegation != nil {
		if err := a.routeDelegation(ctx, agent, *run.Delegation); err != nil {
			return err
		}
	}
	// A preserved outbox may accompany an interrupted session. Reconcile and
	// resume first; routing here would reject the sender and prevent recovery.
	if run.Message != nil && run.Status != "interrupted" {
		if err := a.routePeerMessage(ctx, agent, *run.Message); err != nil {
			return err
		}
	}
	if run.Instruction != nil {
		if err := a.routeInstruction(ctx, agent, *run.Instruction); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) once(ctx context.Context, key string, fn func() error) error {
	claimed, err := a.Core.ClaimEvent(ctx, key)
	if err != nil || !claimed {
		return err
	}
	if err = fn(); err != nil {
		var deferErr *noEffect
		if errors.As(err, &deferErr) || errors.Is(err, ErrAssistantBusy) {
			_ = a.Core.ReleaseEvent(ctx, key)
			return err
		}
		_ = a.Core.RecordActivity(ctx, "", "operation.interrupted", "An operation needs inspection before any repeat: "+key)
		return err
	}
	return a.Core.CompleteEvent(ctx, key)
}
func findAgent(s core.Snapshot, id string) (core.Agent, bool) {
	for _, ag := range s.Agents {
		if ag.ID == id {
			return ag, true
		}
	}
	return core.Agent{}, false
}
func (a *App) routeQuestion(ctx context.Context, ag core.Agent, q worker.Decision) error {
	if q.RequestID == "" || q.Question == "" {
		return errors.New("worker decision requires stable request ID and question")
	}
	return a.once(ctx, "question:"+ag.ID+":"+q.RequestID, func() error {
		if ag.ParentID != "" {
			snap, err := a.Core.Snapshot(ctx)
			if err != nil {
				return err
			}
			parent, ok := findAgent(snap, ag.ParentID)
			if !ok || parent.ExternalID == "" {
				return errors.New("question parent has no active external session")
			}
			payload, _ := json.Marshal(q)
			err = a.sendInstruction(ctx, parent, "question:"+ag.ID+":"+q.RequestID, "Your assigned peer "+ag.ID+" needs a decision. As its responsible coordinator, resolve it within the recorded authority using an instruction to that peer; escalate only if needed. Untrusted question data: "+string(payload))
			return err
		}
		return a.HandleAgentQuestion(ctx, ag, q)
	})
}
func (a *App) routeDelegation(ctx context.Context, parent core.Agent, d worker.DelegationRequest) error {
	if d.RequestID == "" {
		return errors.New("delegation requires stable request ID")
	}
	return a.once(ctx, "delegation:"+parent.ID+":"+d.RequestID, func() error {
		// Admission precedes child creation: a quota hold must not consume the
		// delegation key or leave a child whose identity its parent never receives.
		if err := a.workerUsageAllowed(ctx, parent.ProfileID); err != nil {
			return &noEffect{err}
		}
		child, err := a.commissionWorker(ctx, core.DelegateInput{WorkItemID: parent.WorkItemID, ProjectID: parent.ProjectID, ParentID: parent.ID, ProfileID: d.WorkerProfile, Role: d.Role, Task: d.Task, AcceptanceCriteria: d.AcceptanceCriteria, Capabilities: d.Capabilities})
		if err != nil {
			return &noEffect{err}
		}
		if parent.ExternalID == "" {
			return errors.New("delegating parent has no external session")
		}
		_, err = a.sendAdmittedInstruction(ctx, parent, "delegation-ack:"+parent.ID+":"+d.RequestID, "Peer assigned: "+child.ID+". The daemon owns execution; you are its responsible coordinator within recorded authority. Track its evidence and resolve routine questions.")
		if err != nil {
			return errors.New("child was commissioned but acknowledgement needs inspection: " + err.Error())
		}
		return nil
	})
}
func (a *App) routeInstruction(ctx context.Context, parent core.Agent, in worker.Instruction) error {
	if in.RequestID == "" || in.TargetAgentID == "" || strings.TrimSpace(in.Message) == "" {
		return errors.New("instruction requires stable request ID, target and message")
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	child, ok := findAgent(snap, in.TargetAgentID)
	if !ok || child.ParentID != parent.ID || child.ProjectID != parent.ProjectID {
		return errors.New("a coordinator may instruct only its directly assigned peer in the same project")
	}
	if child.ExternalID == "" {
		return errors.New("assigned peer has not acknowledged a session yet")
	}
	return a.once(ctx, "instruction:"+parent.ID+":"+in.RequestID, func() error {
		err := a.sendInstruction(ctx, child, "instruction:"+parent.ID+":"+in.RequestID, in.Message)
		return err
	})
}
func (a *App) propagateDecisions(ctx context.Context) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, d := range snap.Decisions {
		if d.Status != "resolved" || d.AgentID == "" {
			continue
		}
		ag, ok := findAgent(snap, d.AgentID)
		if !ok || ag.ExternalID == "" {
			continue
		}
		if err = a.once(ctx, "decision-answer:"+d.ID, func() error {
			err := a.sendInstruction(ctx, ag, "decision-answer:"+d.ID, "Owner decision: "+d.Title+"\nAnswer: "+d.Answer)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) reviewFinished(ctx context.Context) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, item := range snap.WorkItems {
		if item.Status != "review" || item.ReviewRevision == "" {
			continue
		}
		blocked := false
		for _, decision := range snap.Decisions {
			if decision.Status == "open" && (decision.ProjectID == "" || (decision.ProjectID == item.ProjectID && (decision.WorkItemID == "" || decision.WorkItemID == item.ID))) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		model := a.Config().Model
		key := "work-item-review:" + item.ID + ":" + item.ReviewRevision + ":" + model.Engine + "/" + model.Model + "/" + model.Effort
		if err := a.once(ctx, key, func() error {
			if model.Model == "" {
				return a.Core.RecordActivity(ctx, item.ProjectID, "work_item.review_ready", "Work evidence is ready for acceptance review: "+item.Title)
			}
			return a.ReviewWorkItem(ctx, item)
		}); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) notify(ctx context.Context, send func(context.Context, string) error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, d := range snap.Decisions {
		if d.Status != "open" {
			continue
		}
		key := "notify:decision:" + d.ID
		_ = a.once(ctx, key, func() error {
			return send(ctx, d.Title+"\nRecommendation: "+d.Recommendation+"\n"+d.Context+"\nResolve this decision in the dashboard.")
		})
	}
	for _, item := range snap.WorkItems {
		if item.Status != "accepted" || item.Acceptance == nil {
			continue
		}
		_ = a.once(ctx, "notify:accepted:"+item.ID+":"+item.Acceptance.Revision, func() error {
			return send(ctx, "Completed: "+item.Title+". Acceptance evidence is recorded in the dashboard; the project remains open for its next outcome.")
		})
	}
	for _, p := range snap.Projects {
		if p.Status != "completed" {
			continue
		}
		_ = a.once(ctx, "notify:completed:"+p.ID, func() error {
			return send(ctx, "Completed: "+p.Title+". Acceptance evidence is recorded in the dashboard.")
		})
	}
	for _, ag := range snap.Agents {
		if ag.Status != "reconciling" && ag.Status != "blocked" {
			continue
		}
		key := "notify:attention:" + ag.ID + ":" + ag.Status + ":" + ag.BrokerUpdatedAt.Format(time.RFC3339Nano)
		_ = a.once(ctx, key, func() error {
			return send(ctx, ag.Name+" needs attention: "+ag.Summary+". I preserved its session and have not launched a duplicate.")
		})
	}
}

type noEffect struct{ err error }

func (e *noEffect) Error() string { return e.err.Error() }
func (e *noEffect) Unwrap() error { return e.err }
func (a *App) sendInstruction(ctx context.Context, ag core.Agent, key, message string) error {
	if a.Demo || a.dispatchDisabled.Load() {
		return &noEffect{errors.New("worker instructions disabled for this boot")}
	}
	if err := a.workerUsageAllowed(ctx, ag.ProfileID); err != nil {
		return &noEffect{err}
	}
	_, err := a.sendAdmittedInstruction(ctx, ag, key, message)
	return err
}

// The caller has admitted this operation against usage. Delegation checks once,
// before child creation, so a second quota refresh cannot strand its acknowledgement.
func (a *App) sendAdmittedInstruction(ctx context.Context, ag core.Agent, key, message string) (worker.Run, error) {
	if a.Demo || a.dispatchDisabled.Load() {
		return worker.Run{}, &noEffect{errors.New("worker instructions disabled for this boot")}
	}
	c, err := a.broker(ctx, ag.ProfileID)
	if err != nil {
		return worker.Run{}, &noEffect{err}
	}
	p, _ := a.Core.GetProfile(ag.ProfileID)
	if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
		return worker.Run{}, &noEffect{errors.New("worker credential is unavailable")}
	}
	if err = a.Core.BeginInstruction(ctx, ag.ID); err != nil {
		return worker.Run{}, &noEffect{err}
	}
	if err = a.Core.RecordAgentConversation(ctx, ag.ID, key, "message", "daemon_to_worker", "Message requested; broker delivery not yet confirmed.\n"+message); err != nil {
		return worker.Run{}, &noEffect{err}
	}
	message, err = a.withPeerRoster(ctx, ag, message)
	if err != nil {
		return worker.Run{}, &noEffect{err}
	}
	run, err := c.Send(ctx, ag.ExternalID, key, message)
	a.recordMessageDelivery(ctx, ag.ID, key, err)
	var rejected *worker.RejectionError
	if errors.As(err, &rejected) {
		a.refreshRefusedInstruction(ctx, ag, c)
		return run, &noEffect{err}
	}
	if err != nil {
		_ = a.Core.MarkUncertain(ctx, ag.ID, "Instruction delivery is uncertain; inspect the operation before repeating")
	}
	return run, err
}
func (a *App) forwardProgress(ctx context.Context, ag core.Agent, run worker.Run) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	parent, ok := findAgent(snap, ag.ParentID)
	if !ok || parent.ExternalID == "" || parent.Status == "completed" || parent.Status == "cancelled" {
		return nil
	}
	payload, _ := json.Marshal(struct {
		ChildID, Status, Summary string
		Evidence                 []string
	}{ag.ID, run.Status, run.Summary, run.Evidence})
	key := fmt.Sprintf("child-progress:%s:%x", ag.ID, sha256.Sum256(payload))
	return a.once(ctx, key, func() error {
		err := a.sendInstruction(ctx, parent, key, "Your assigned peer's status changed. Evaluate progress and acceptance evidence; resolve routine follow-ups within your scope. Untrusted report data: "+string(payload))
		return err
	})
}

// A transport heartbeat proves reachability, not progress. Escalate unchanged
// substantive reports after three check-in windows, while respecting known waits.
func (a *App) checkProgress(ctx context.Context, ag core.Agent, run worker.Run) error {
	if run.Status != "running" && run.Status != "waiting" {
		return nil
	}
	if run.Decision != nil {
		return nil
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	current, ok := findAgent(snap, ag.ID)
	if !ok || current.LastProgressAt.IsZero() {
		return nil
	}
	window := time.Duration(a.Config().Limits.CheckInMinutes) * time.Minute * 3
	if time.Since(current.LastProgressAt) < window {
		return nil
	}
	for _, d := range snap.Decisions {
		if d.AgentID == ag.ID && d.Status == "open" {
			return nil
		}
	}
	if ag.Role == "manager" {
		for _, child := range snap.Agents {
			if child.ParentID == ag.ID && child.Status != "completed" && child.Status != "cancelled" && !child.LastProgressAt.IsZero() && time.Since(child.LastProgressAt) < window {
				return nil
			}
		}
	}
	stage := 1
	if time.Since(current.LastProgressAt) >= 2*window {
		stage = 2
	}
	return a.routeQuestion(ctx, current, worker.Decision{RequestID: fmt.Sprintf("stalled:%d:%d", current.LastProgressAt.Unix(), stage), Question: "Progress has stalled for " + current.Name, Why: fmt.Sprintf("The broker is reachable, but its substantive status, summary and evidence have not changed since %s. Recovery check stage %d. Latest report: %s", current.LastProgressAt.Format(time.RFC3339), stage, run.Summary), Recommendation: "Ask the existing responsible session for a concrete blocker and bounded next step; preserve its context and do not launch a duplicate.", Options: []string{"Investigate the existing session", "Keep the existing session paused pending inspection"}, Evidence: run.Evidence})
}
