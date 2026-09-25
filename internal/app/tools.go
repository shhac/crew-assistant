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

func (a *App) Execute(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "list_connections", "query_connection":
		return a.runConnectionTool(ctx, name, raw)
	case "read_state":
		if err := args(raw, &struct{}{}); err != nil {
			return nil, err
		}
		state, _, err := a.context(ctx)
		if err != nil {
			return nil, err
		}
		return state, nil
	case "create_project":
		var in engine.CreateProjectArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.CreateProject(ctx, core.ProjectInput{Directories: in.Directories, Title: in.Title, Template: in.Template, Brief: core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria}})
	case "update_brief":
		var in engine.UpdateBriefArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		project, err := a.Core.UpdateBrief(ctx, in.ProjectID, core.BriefInput{Goal: in.Goal, Audience: in.Audience, Constraints: in.Constraints, Criteria: in.Criteria})
		if err == nil {
			a.Work.Nudge()
		}
		return project, err
	case "set_team":
		var in engine.SetTeamArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.SetTeam(ctx, in.ProjectID, work.TeamChoice{Template: in.Template, WriterEngine: in.WriterEngine, ReviewerEngine: in.ReviewerEngine, MaxRounds: in.MaxRounds, DeliverTo: in.DeliverTo, Repo: in.Repo, BranchPrefix: in.BranchPrefix, Check: in.Check, Prepare: in.Prepare, Sign: in.Sign, Implementer: in.ImplementerMember, Reviewer: in.ReviewerMember, QA: in.QAMember, Planner: in.PlannerMember, PM: in.PMMember})
	case "draw_member":
		var in engine.DrawMemberArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if err := a.DrawMember(ctx, in.MemberID, in.Look); err != nil {
			return nil, err
		}
		return map[string]string{"status": "Drawing; it takes a few minutes."}, nil
	case "record_learning":
		var in engine.RecordLearningArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.AddLearning(ctx, in.MemberID, core.LearnedByAssistant, core.LearningInput{When: in.When, Text: in.Text, ProjectID: in.ProjectID})
	case "set_landing":
		var in engine.SetLandingArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.SetLanding(ctx, in.ProjectID, core.LandPolicy{Means: in.Means, Via: in.Via, Target: in.Target, Method: in.Method, GitHub: in.GitHub, Approve: in.Approve})
	case "land_task":
		var in engine.LandTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.LandTask(ctx, in.ProjectID, in.TaskID)
	case "wake_me_when":
		var in engine.WakeArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.WakeMeWhen(ctx, work.WakeRequest(in))
	case "list_wakes":
		if err := args(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return a.Work.OpenWakes(ctx)
	case "cancel_wake":
		var in engine.WakeHandleArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.CancelWake(ctx, in.Handle, "")
	case "queue_task":
		var in engine.QueueTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		queued, err := a.Core.QueueTask(ctx, in.ProjectID, core.TaskInput{Objective: in.Objective, Criteria: in.Criteria, DependsOn: in.DependsOn})
		if err == nil {
			a.Work.Nudge()
		}
		return queued, err
	case "stop_task":
		var in engine.StopTaskArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.StopTask(ctx, in.ProjectID, in.TaskID)
	case "order_tasks":
		var in engine.OrderTasksArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.OrderTasks(ctx, in.ProjectID, in.TaskIDs, core.OrderedByAssistant)
	case "message_team":
		var in engine.MessageTeamArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Work.MessageTeam(ctx, in.ProjectID, in.TaskID, in.To, core.FromAssistant, in.Message)
	case "resolve_decision":
		var in engine.ResolveDecisionArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		if (strings.TrimSpace(in.Choice) == "") == (strings.TrimSpace(in.Answer) == "") {
			return nil, errors.New("give either a choice or an answer")
		}
		choose, answer := a.Core.ChooseDecision, in.Choice
		if strings.TrimSpace(in.Answer) != "" {
			choose, answer = a.Core.AnswerDecision, in.Answer
		}
		decision, err := choose(ctx, in.DecisionID, answer)
		if err == nil {
			a.Work.Nudge()
		}
		return decision, err
	case "ask_decision":
		var in engine.DecisionArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		why := in.Why
		if len(in.Evidence) > 0 {
			why += "\nEvidence: " + strings.Join(in.Evidence, "; ")
		}
		return a.Core.CreateDecision(ctx, core.DecisionInput{ProjectID: in.ProjectID, Title: in.Question, Context: why, Recommendation: in.Recommendation, Choices: in.Options})
	case "remember_preference":
		var in engine.PreferenceArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.Core.Remember(ctx, in.Key, in.Value)
	case "manage_conversation":
		var in engine.ManageConversationArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return a.manageConversation(ctx, in)
	case "report_status":
		var in engine.StatusArgs
		if err := args(raw, &in); err != nil {
			return nil, err
		}
		return map[string]bool{"recorded": true}, a.Core.RecordActivity(ctx, in.ProjectID, "assistant.update", in.Summary+evidenceText(in.Evidence))
	default:
		return nil, errors.New("unavailable coordination action")
	}
}

func evidenceText(e []string) string {
	if len(e) == 0 {
		return ""
	}
	return "\nEvidence: " + strings.Join(e, "; ")
}
