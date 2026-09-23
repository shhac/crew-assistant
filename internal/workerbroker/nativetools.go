package workerbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// The worker's whole tool surface. The native harness runs its own agent loop
// and calls these over the daemon's private channel; every one of them executes
// inside the offline container, or asks the daemon to do something on the
// worker's behalf. Nothing here reaches the host.
//
// The three that end an assignment are marked Closing: the harness library
// serializes tool execution and shuts the channel once one of them succeeds, so
// a report cannot land while a write or a test is still running.
func workerToolDefinitions() []session.ToolDefinition {
	return []session.ToolDefinition{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file by relative path inside the isolated /workspace copy.",
			Schema:      objectSchema(map[string]string{"path": "Relative path inside the workspace."}, "path"),
		},
		{
			Name:        "write_file",
			Description: "Write a UTF-8 text file by relative path inside the isolated /workspace copy, creating parent directories. Replaces the whole file.",
			Schema:      objectSchema(map[string]string{"path": "Relative path inside the workspace.", "content": "Complete new file contents."}, "path", "content"),
		},
		{
			Name:        "run_command",
			Description: "Run one build, test or inspection command inside the offline container. There is no network. Never install dependencies or contact remote services. Output and time are bounded and every run is recorded as evidence.",
			Schema:      objectSchema(map[string]string{"command": "Shell command interpreted by the container's /bin/sh."}, "command"),
		},
		{
			Name:        "acknowledge_steering",
			Description: "Record which daemon-provided steering messages you have read. This is a read receipt: it is not evidence of implementation and grants no permission to change scope.",
			Schema:      arraySchema("message_ids", "Steering message identifiers supplied by the daemon."),
		},
		{
			Name:        "send_message",
			Description: "Ask the daemon to deliver bounded task information to another agent on this project. This ends your turn until the daemon acknowledges routing. It grants no authority; use ask_decision for approval or scope changes.",
			Schema:      objectSchema(map[string]string{"target_agent_id": "Another agent in the daemon-provided project roster.", "message": "Task information, 1-8000 bytes."}, "target_agent_id", "message"),
			Closing:     true,
		},
		{
			Name:        "ask_decision",
			Description: "Stop and ask the responsible coordinator one concrete question you cannot resolve. Requires a recommendation, the reason, and at least two alternatives.",
			Schema: withArrays(objectSchema(map[string]string{
				"question":       "The single unresolved question.",
				"recommendation": "What you would do absent an answer.",
				"why":            "Why this needs a decision rather than a judgement call.",
			}, "question", "recommendation", "why"), map[string]string{
				"options":  "At least two concrete alternatives.",
				"evidence": "Bounded supporting evidence.",
			}, "options"),
			Closing: true,
		},
		{
			Name:        "finish",
			Description: "Report an acceptance summary after real changes and real checks. The daemon independently collects the patch and command evidence, and the assistant decides whether to accept; calling this does not accept anything.",
			Schema:      objectSchema(map[string]string{"summary": "What you changed and what you verified."}, "summary"),
			Closing:     true,
		},
	}
}

func objectSchema(properties map[string]string, required ...string) map[string]any {
	props := map[string]any{}
	for name, description := range properties {
		props[name] = map[string]any{"type": "string", "description": description}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func arraySchema(name, description string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{name: map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}},
		"required":   []string{name}, "additionalProperties": false,
	}
}

func withArrays(schema map[string]any, arrays map[string]string, required ...string) map[string]any {
	props := schema["properties"].(map[string]any)
	for name, description := range arrays {
		props[name] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	schema["required"] = append(schema["required"].([]string), required...)
	return schema
}

// workerTools executes the worker's tools for one run. The harness serializes
// calls, so this never sees two at once; that is what lets an acceptance report
// be about a settled workspace rather than one still being written to.
type workerTools struct {
	broker    *Broker
	id        string
	container string
	implement bool
	agentID   string
}

func (w *workerTools) CallTool(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
	switch call.Name {
	case "read_file":
		return w.readFile(ctx, call.Arguments)
	case "write_file":
		return w.writeFile(ctx, call.Arguments)
	case "run_command":
		return w.runCommand(ctx, call.Arguments)
	case "acknowledge_steering":
		return w.acknowledge(call.Arguments)
	case "send_message":
		return w.sendMessage(call.Arguments)
	case "ask_decision":
		return w.askDecision(call.Arguments)
	case "finish":
		return w.finish(call.Arguments)
	}
	return refusal("tool %q is not available to this worker", call.Name), nil
}

func refusal(format string, args ...any) session.ToolResult {
	return session.ToolResult{Content: fmt.Sprintf(format, args...), IsError: true}
}

func encoded(value any) session.ToolResult {
	raw, err := json.Marshal(value)
	if err != nil {
		return refusal("result could not be encoded")
	}
	return session.ToolResult{Content: string(raw)}
}

func (w *workerTools) readFile(ctx context.Context, arguments json.RawMessage) (session.ToolResult, error) {
	var in struct {
		Path string `json:"path"`
	}
	if strict(arguments, &in) != nil {
		return refusal("read_file takes a single relative path"), nil
	}
	path, err := safePath(in.Path)
	if err != nil {
		return refusal("%s", err.Error()), nil
	}
	c, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	out, err := w.broker.cfg.Command.Run(c, []string{"exec", w.container, "/bin/sh", "-c", "cat -- \"$1\"", "read", path}, nil)
	if err != nil {
		return refusal("could not read %s inside the isolated workspace", in.Path), nil
	}
	return encoded(map[string]any{"path": in.Path, "content": string(out), "output_limit_bytes": 65536}), nil
}

// heldByUnsettledCommand refuses anything that changes the workspace while a
// command that outlived its limit has not been confirmed stopped.
//
// Cancelling `docker exec` ends the local client; the process it started may
// still be running inside the container, still writing. A worker that keeps
// editing on top of that is building on a workspace two things are changing at
// once, and neither the patch nor the command log afterwards would describe what
// actually happened. Reading stays available, because re-reading is exactly how
// a worker can find out what it is dealing with.
func (w *workerTools) heldByUnsettledCommand(action string) *session.ToolResult {
	run, err := w.broker.snapshot(w.id)
	if err != nil || run.UnsettledCommands == 0 {
		return nil
	}
	held := refusal("%d command(s) passed their time limit and were never confirmed stopped, so something may still be writing to this workspace. %s is refused until that is settled. Read files to find out what state they are in, and report what you find rather than changing more.", run.UnsettledCommands, action)
	return &held
}

func (w *workerTools) writeFile(ctx context.Context, arguments json.RawMessage) (session.ToolResult, error) {
	if !w.implement {
		return refusal("this worker may review but not change files"), nil
	}
	if held := w.heldByUnsettledCommand("Writing"); held != nil {
		return *held, nil
	}
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if strict(arguments, &in) != nil {
		return refusal("write_file takes a relative path and complete file contents"), nil
	}
	if len(in.Content) > 256*1024 {
		return refusal("file contents exceed the 256 KiB write limit"), nil
	}
	path, err := safePath(in.Path)
	if err != nil {
		return refusal("%s", err.Error()), nil
	}
	c, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	_, err = w.broker.cfg.Command.Run(c, []string{"exec", "--interactive", w.container, "/bin/sh", "-c", "mkdir -p -- \"$(dirname -- \"$1\")\" && cat > \"$1\"", "write", path}, []byte(in.Content))
	if err != nil {
		return refusal("could not write %s inside the isolated workspace", in.Path), nil
	}
	_ = w.broker.progress(w.id, "Updated "+in.Path+" in the isolated workspace")
	w.broker.recordActivity(w.id, activityEntry{Kind: "tool", Tool: "write_file", Status: "completed", Detail: in.Path})
	return encoded(map[string]string{"written": in.Path}), nil
}

// runCommand runs one command inside the container.
//
// The time limit is the container's, not the client's. Cancelling `docker exec`
// ends the local client; the process it started keeps running inside the
// container, where it can still be writing to the workspace long after this
// call has reported a timeout. So the limit is enforced by the shell that runs
// the command, and a client-side cancellation is reported as an unknown
// outcome that has to be settled before the workspace is described again.
func (w *workerTools) runCommand(ctx context.Context, arguments json.RawMessage) (session.ToolResult, error) {
	var in struct {
		Command string `json:"command"`
	}
	if strict(arguments, &in) != nil || strings.TrimSpace(in.Command) == "" || len(in.Command) > 8000 {
		return refusal("run_command takes one non-empty command of at most 8000 bytes"), nil
	}
	if held := w.heldByUnsettledCommand("Running another command"); held != nil {
		return *held, nil
	}
	// `timeout` belongs to the container image; if it is missing the command
	// still runs, and the client-side bound below is what stops the wait. The
	// difference is reported rather than assumed either way.
	contained := fmt.Sprintf("command -v timeout >/dev/null 2>&1 && exec timeout -s KILL %d /bin/sh -c \"$1\" || exec /bin/sh -c \"$1\"", commandLimitSeconds)
	// The client waits slightly longer than the container's own limit, so an
	// in-container timeout is observed as a finished command rather than as a
	// cancelled client.
	c, stop := context.WithTimeout(ctx, (commandLimitSeconds+15)*time.Second)
	defer stop()
	out, err := w.broker.cfg.Command.Run(c, []string{"exec", w.container, "/bin/sh", "-c", contained, "run", in.Command}, nil)
	record := commandRecord{Command: in.Command, Success: err == nil, Output: string(out)}
	// A cancelled client says nothing about the process inside the container.
	uncertain := c.Err() != nil
	if uncertain {
		record.Success = false
		record.Output += "\n[the command exceeded its limit and the client stopped waiting; whether it is still running in the container is unknown]"
	}
	if saveErr := w.broker.update(w.id, func(run *storedRun) error {
		run.Commands = append(run.Commands, record)
		run.Run.Summary = "Ran isolated command: " + in.Command
		if uncertain {
			run.UnsettledCommands++
		}
		run.Run.UpdatedAt = now()
		return nil
	}); saveErr != nil {
		return session.ToolResult{}, saveErr
	}
	status := "completed"
	if !record.Success {
		status = "failed"
	}
	if uncertain {
		status = "unsettled"
	}
	w.broker.recordActivity(w.id, activityEntry{Kind: "tool", Tool: "run_command", Status: status, Detail: in.Command})
	if uncertain {
		return encoded(map[string]any{
			"command": in.Command, "success": false, "output": record.Output,
			"note": "this command passed its time limit and the daemon stopped waiting for it. Whether it is still running inside the container is unknown, so treat the workspace as uncertain: re-read anything it may have written before relying on it, and say so if you report.",
		}), nil
	}
	return encoded(map[string]any{"command": in.Command, "success": record.Success, "output": record.Output}), nil
}

// commandLimitSeconds bounds one command inside the container. Codex's own MCP
// client gives a hosted tool 60 seconds by default, so a longer limit here would
// be reported to the model as a tool timeout while the command carried on — an
// outcome neither side could describe. Keeping the container's limit under that
// keeps the two agreeing about what happened.
const commandLimitSeconds = 45

func (w *workerTools) acknowledge(arguments json.RawMessage) (session.ToolResult, error) {
	value, _, err := w.broker.acknowledgeSteering(w.id, string(arguments))
	if err != nil {
		return refusal("%s", err.Error()), nil
	}
	return encoded(value), nil
}

func (w *workerTools) sendMessage(arguments json.RawMessage) (session.ToolResult, error) {
	var in struct {
		TargetAgentID string `json:"target_agent_id"`
		Message       string `json:"message"`
	}
	if strict(arguments, &in) != nil || strings.TrimSpace(in.TargetAgentID) == "" || in.TargetAgentID == w.agentID || strings.TrimSpace(in.Message) == "" || len(in.Message) > 8000 {
		return refusal("send_message needs another agent on this project and 1-8000 bytes of content"), nil
	}
	request := worker.PeerMessage{RequestID: uid(), TargetAgentID: in.TargetAgentID, Message: in.Message}
	err := w.broker.update(w.id, func(run *storedRun) error {
		if run.PendingStatus == "cancelled" {
			return errInterrupted
		}
		if run.Run.Message != nil || run.PendingMessage != nil {
			return errors.New("a previous peer message still awaits daemon acknowledgement")
		}
		run.PendingMessage = &request
		run.PendingStatus = "waiting"
		run.PendingSummary = "Waiting for the daemon to route a peer message"
		return nil
	})
	if err != nil {
		return refusal("%s", err.Error()), nil
	}
	w.broker.recordActivity(w.id, activityEntry{Kind: "coordination", Tool: "send_message", Status: "pending", Detail: "peer message queued for daemon delivery"})
	return encoded(map[string]string{"status": "pending_daemon_delivery", "request_id": request.RequestID}), nil
}

func (w *workerTools) askDecision(arguments json.RawMessage) (session.ToolResult, error) {
	var in worker.Decision
	if strict(arguments, &in) != nil || in.Question == "" || in.Recommendation == "" || in.Why == "" || len(in.Options) < 2 {
		return refusal("ask_decision needs a question, a recommendation, the reason, and at least two alternatives"), nil
	}
	in.RequestID = uid()
	err := w.broker.update(w.id, func(run *storedRun) error {
		if run.PendingStatus == "cancelled" {
			return errInterrupted
		}
		run.PendingStatus = "blocked"
		run.PendingSummary = in.Question
		run.Run.Decision = &in
		run.Run.UpdatedAt = now()
		return nil
	})
	if err != nil {
		return refusal("%s", err.Error()), nil
	}
	w.broker.recordActivity(w.id, activityEntry{Kind: "coordination", Tool: "ask_decision", Status: "blocked", Detail: in.Question})
	return encoded(map[string]string{"status": "blocked", "next": "the coordinator's answer arrives in this same session when the assignment resumes"}), nil
}

func (w *workerTools) finish(arguments json.RawMessage) (session.ToolResult, error) {
	var in struct {
		Summary string `json:"summary"`
	}
	if strict(arguments, &in) != nil || strings.TrimSpace(in.Summary) == "" {
		return refusal("finish needs an acceptance summary describing what changed and what was verified"), nil
	}
	// A command that outlived its limit may still be writing. Reporting the work
	// as done on top of that would be describing a workspace that is still
	// moving, so the assignment stops for inspection instead.
	if run, err := w.broker.snapshot(w.id); err == nil && run.UnsettledCommands > 0 {
		w.broker.terminal(w.id, "blocked", fmt.Sprintf("The worker reported completion, but %d command(s) passed their time limit and were never confirmed stopped, so the workspace may still be changing. Inspect the container and the preserved evidence before accepting. The worker's summary was: %s", run.UnsettledCommands, excerpt(in.Summary, 2000)))
		w.broker.recordActivity(w.id, activityEntry{Kind: "coordination", Tool: "finish", Status: "unsettled", Detail: in.Summary})
		return encoded(map[string]string{
			"status": "held", "reason": "a command passed its time limit and was never confirmed stopped; the daemon has held this assignment for inspection rather than reporting it complete",
		}), nil
	}
	w.broker.terminal(w.id, "completed", in.Summary)
	w.broker.recordActivity(w.id, activityEntry{Kind: "coordination", Tool: "finish", Status: "reported", Detail: in.Summary})
	return encoded(map[string]string{
		"status":     "reported_complete",
		"acceptance": "the daemon now collects the actual patch and command evidence; the assistant decides whether to accept",
	}), nil
}
