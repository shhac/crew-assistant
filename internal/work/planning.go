package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// planTask has the team's planner work out what a task needs before anything
// is written. The planner only reads, starts afresh every time, and leaves
// its plan on the task, where the implementer and the reviewers read it.
func (lp *Loop) planTask(ctx context.Context, p core.Project, t core.Task, m medium) error {
	planner, ok := t.Planner()
	if !ok {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	// A plan recorded before a restart is not paid for twice.
	if t.Plan != nil {
		return lp.askPlanQuestions(ctx, t)
	}
	if held, err := lp.holdForUsage(ctx, t, planner); held || err != nil {
		return err
	}
	t, err := lp.prepareWorkspace(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	base := plannerPrompt(p, t, otherWork(snap, t)) + learnedGuide(planner, true)
	spec, cleanup, err := lp.roleSpec(t, planner, m.workspace(), false, m, base)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	var plan core.Plan
	var dependsOn []string
	var reply string
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return lp.roleFailed(ctx, t, planner.Name, err)
		}
		var learned string
		reply, learned = splitBlock(result.Text, "learned")
		lp.recordLearned(ctx, p, t, planner, m, learned)
		if plan, dependsOn, err = parsePlan(reply); err == nil {
			break
		}
		spec.Prompt = retryPrompt(base, err)
	}
	// A plan that still cannot be read is kept as written rather than stopping
	// the work: the implementer reads it either way.
	if plan.Summary == "" {
		plan, dependsOn = core.Plan{Summary: text.Clip(strings.TrimSpace(reply), 3000)}, nil
	}
	plan.Role = planner.Name
	planned, err := lp.Core.RecordPlan(ctx, t.ID, plan, dependsOn)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	if err != nil || planned.Status != core.TaskPlanning {
		return err
	}
	return lp.askPlanQuestions(ctx, planned)
}

// askPlanQuestions brings the planner's questions to the owner before
// anything is written.
func (lp *Loop) askPlanQuestions(ctx context.Context, t core.Task) error {
	if t.Plan == nil || len(t.Plan.Questions) == 0 {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionQuestion, core.DecisionInput{
		Title:          fmt.Sprintf("%s has questions about “%s” before starting", t.Plan.Role, t.Objective),
		Context:        strings.TrimSpace(numbered(t.Plan.Questions)),
		Recommendation: "Answer what you can, or let the team use its judgment",
		Choices:        []string{"Use your judgment", choiceStop},
	})
	return err
}

// otherWork is the project's other unfinished tasks, which a plan may say
// this one has to wait for.
func otherWork(snap core.Snapshot, t core.Task) []core.Task {
	var out []core.Task
	for _, other := range snap.Tasks {
		if other.ProjectID == t.ProjectID && other.ID != t.ID && !other.Finished() {
			out = append(out, other)
		}
	}
	return out
}

// parsePlan reads the planner's JSON answer, tolerating a fenced block or
// surrounding prose, and bounds what it keeps.
func parsePlan(reply string) (core.Plan, []string, error) {
	var in struct {
		Summary    string   `json:"summary"`
		Exists     []string `json:"exists"`
		Changes    []string `json:"changes"`
		OutOfScope []string `json:"out_of_scope"`
		Questions  []string `json:"questions"`
		DependsOn  []string `json:"depends_on"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.Plan{}, nil, errors.New("the plan was not valid JSON")
	}
	if strings.TrimSpace(in.Summary) == "" {
		return core.Plan{}, nil, errors.New("the plan had no summary")
	}
	list := func(items []string) []string { return listed(items, 20) }
	return core.Plan{
		Summary:    text.Clip(strings.TrimSpace(in.Summary), 3000),
		Exists:     list(in.Exists),
		Changes:    list(in.Changes),
		OutOfScope: list(in.OutOfScope),
		Questions:  list(in.Questions),
	}, list(in.DependsOn), nil
}
