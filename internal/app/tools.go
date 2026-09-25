package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/work"
)

func args(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing arguments")
	}
	return nil
}

// Execute carries out a tool call the assistant made, with its arguments
// decoded strictly first. Model arguments grant nothing: each action checks
// what it may do for itself.
func (a *App) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	act, ok := toolActions[name]
	if !ok {
		return nil, errors.New("unavailable coordination action")
	}
	return act(a, ctx, raw)
}

// toolAction carries out one tool's call.
type toolAction func(a *App, ctx context.Context, raw json.RawMessage) (any, error)

// with decodes a call's arguments as T before acting on them.
func with[T any](act func(a *App, ctx context.Context, in T) (any, error)) toolAction {
	return func(a *App, ctx context.Context, raw json.RawMessage) (any, error) {
		var in T
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return act(a, ctx, in)
	}
}

// none is a tool that takes no arguments.
type none struct{}

var toolActions = map[string]toolAction{
	"list_connections": func(a *App, ctx context.Context, raw json.RawMessage) (any, error) {
		return a.runConnectionTool(ctx, "list_connections", raw)
	},
	"query_connection": func(a *App, ctx context.Context, raw json.RawMessage) (any, error) {
		return a.runConnectionTool(ctx, "query_connection", raw)
	},
	"read_state": with(func(a *App, ctx context.Context, _ none) (any, error) {
		state, _, err := a.chatContext(ctx, "")
		if err != nil {
			return nil, err
		}
		return state, nil
	}),
	"read_task": with(func(a *App, ctx context.Context, in engine.ReadTaskArgs) (any, error) {
		s, err := a.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range s.Tasks {
			if t.ID == in.TaskID && t.ProjectID == in.ProjectID {
				return taskDetail(t), nil
			}
		}
		return nil, core.ErrNotFound
	}),
	"create_project": with(func(a *App, ctx context.Context, in engine.CreateProjectArgs) (any, error) {
		return a.Core.CreateProject(ctx, core.ProjectInput{Directories: in.Directories, Title: in.Title, Template: in.Template, Brief: core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria}})
	}),
	"update_brief": with(func(a *App, ctx context.Context, in engine.UpdateBriefArgs) (any, error) {
		return a.Work.UpdateBrief(ctx, in.ProjectID, core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria})
	}),
	"set_team": with(func(a *App, ctx context.Context, in engine.SetTeamArgs) (any, error) {
		return a.Work.SetTeam(ctx, in.ProjectID, work.TeamChoice{Template: in.Template, WriterEngine: in.WriterEngine, ReviewerEngine: in.ReviewerEngine, MaxRounds: in.MaxRounds, DeliverTo: in.DeliverTo, Repo: in.Repo, BranchPrefix: in.BranchPrefix, Check: in.Check, Prepare: in.Prepare, Sign: in.Sign, Implementer: in.ImplementerMember, Reviewer: in.ReviewerMember, QA: in.QAMember, Researcher: in.ResearcherMember, Designer: in.DesignerMember, PM: in.PMMember})
	}),
	"set_landing": with(func(a *App, ctx context.Context, in engine.SetLandingArgs) (any, error) {
		return a.Work.SetLanding(ctx, in.ProjectID, core.LandPolicy{Means: in.Means, Via: in.Via, Target: in.Target, Method: in.Method, GitHub: in.GitHub, Approve: in.Approve})
	}),
	"queue_task": with(func(a *App, ctx context.Context, in engine.QueueTaskArgs) (any, error) {
		return a.Work.QueueTask(ctx, in.ProjectID, core.TaskInput{Objective: in.Objective, Criteria: in.Criteria, DependsOn: in.DependsOn}, core.LinkedByAssistant)
	}),
	"link_tasks": with(func(a *App, ctx context.Context, in engine.LinkTasksArgs) (any, error) {
		return a.Work.LinkTasks(ctx, in.ProjectID, in.TaskID, in.Relation, in.OtherTaskID, core.LinkedByAssistant)
	}),
	"unlink_tasks": with(func(a *App, ctx context.Context, in engine.UnlinkTasksArgs) (any, error) {
		return a.Work.UnlinkTasks(ctx, in.ProjectID, in.TaskID, in.OtherTaskID, core.LinkedByAssistant)
	}),
	"order_tasks": with(func(a *App, ctx context.Context, in engine.OrderTasksArgs) (any, error) {
		return a.Core.OrderTasks(ctx, in.ProjectID, in.TaskIDs, core.OrderedByAssistant)
	}),
	"stop_task": with(func(a *App, ctx context.Context, in engine.StopTaskArgs) (any, error) {
		return a.Work.StopTask(ctx, in.ProjectID, in.TaskID)
	}),
	"land_task": with(func(a *App, ctx context.Context, in engine.LandTaskArgs) (any, error) {
		return a.Work.LandTask(ctx, in.ProjectID, in.TaskID)
	}),
	"message_team": with(func(a *App, ctx context.Context, in engine.MessageTeamArgs) (any, error) {
		return a.Work.MessageTeam(ctx, in.ProjectID, in.TaskID, in.To, core.FromAssistant, in.Message)
	}),
	"ask_pm": with(func(a *App, ctx context.Context, in engine.AskPMArgs) (any, error) {
		answer, err := a.Work.AskPM(ctx, in.ProjectID, in.Question)
		if err != nil {
			return nil, err
		}
		return map[string]string{"answer": answer}, nil
	}),
	"resolve_decision": with(func(a *App, ctx context.Context, in engine.ResolveDecisionArgs) (any, error) {
		return a.Work.ResolveDecision(ctx, in.DecisionID, in.Choice, in.Answer)
	}),
	"ask_decision": with(func(a *App, ctx context.Context, in engine.DecisionArgs) (any, error) {
		return a.Core.CreateDecision(ctx, core.DecisionInput{ProjectID: in.ProjectID, Title: in.Question, Context: in.Why + evidenceText(in.Evidence), Recommendation: in.Recommendation, Choices: in.Options})
	}),
	"wake_me_when": with(func(a *App, ctx context.Context, in engine.WakeArgs) (any, error) {
		return a.Work.WakeMeWhen(ctx, work.WakeRequest(in))
	}),
	"list_wakes": with(func(a *App, ctx context.Context, _ none) (any, error) {
		return a.Work.OpenWakes(ctx)
	}),
	"cancel_wake": with(func(a *App, ctx context.Context, in engine.WakeHandleArgs) (any, error) {
		return a.Core.CancelWake(ctx, in.Handle, "")
	}),
	"draw_member": with(func(a *App, ctx context.Context, in engine.DrawMemberArgs) (any, error) {
		if err := a.DrawMember(ctx, in.MemberID, in.Look); err != nil {
			return nil, err
		}
		return map[string]string{"status": "Drawing; it takes a few minutes."}, nil
	}),
	"record_learning": with(func(a *App, ctx context.Context, in engine.RecordLearningArgs) (any, error) {
		return a.Core.AddLearning(ctx, in.MemberID, core.LearnedByAssistant, core.LearningInput{When: in.When, Text: in.Text, ProjectID: in.ProjectID})
	}),
	"remember_preference": with(func(a *App, ctx context.Context, in engine.PreferenceArgs) (any, error) {
		return a.Core.Remember(ctx, in.Key, in.Value)
	}),
	"manage_conversation": with(func(a *App, ctx context.Context, in engine.ManageConversationArgs) (any, error) {
		return a.manageConversation(ctx, in)
	}),
	"report_status": with(func(a *App, ctx context.Context, in engine.StatusArgs) (any, error) {
		return map[string]bool{"recorded": true}, a.Core.RecordActivity(ctx, in.ProjectID, "assistant.update", in.Summary+evidenceText(in.Evidence))
	}),
}

func evidenceText(e []string) string {
	if len(e) == 0 {
		return ""
	}
	return "\nEvidence: " + strings.Join(e, "; ")
}
