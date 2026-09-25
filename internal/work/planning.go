package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// researchTask has the team's researcher work out what a task needs before
// anything is written. The researcher only reads, starts afresh every time,
// and leaves its plan on the task, where the implementer and the reviewers
// read it. It may hand the task to the designer first, for design input.
func (lp *Loop) researchTask(ctx context.Context, p core.Project, t core.Task, m medium) error {
	researcher, ok := t.Researcher()
	if !ok {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	// A plan recorded before a restart is not paid for twice.
	if t.Plan != nil {
		return lp.askPlanQuestions(ctx, t)
	}
	if held, err := lp.holdForUsage(ctx, t, researcher); held || err != nil {
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
	base := researcherPrompt(p, t, otherWork(snap, t)) + learnedGuide(researcher, true)
	spec, cleanup, err := lp.roleSpec(t, researcher, m.workspace(), false, m, base)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	var plan core.Plan
	var dependsOn []string
	var design string
	reply, learned, _, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		plan, dependsOn, design, err = parsePlan(reply, designsFor(t, researcher))
		return err
	})
	if err != nil {
		return lp.roleFailed(ctx, t, researcher.Name, err)
	}
	lp.recordLearned(ctx, p, t, researcher, m, learned)
	if design != "" {
		return lp.askDesign(ctx, t, researcher.Name, design, nil)
	}
	// A plan that still cannot be read is kept as written rather than stopping
	// the work: the implementer reads it either way.
	if plan.Summary == "" {
		plan, dependsOn = core.Plan{Summary: text.Clip(strings.TrimSpace(reply), 3000)}, nil
	}
	plan.Role = researcher.Name
	planned, err := lp.Core.RecordPlan(ctx, t.ID, plan, dependsOn)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	if err != nil || planned.Status != core.TaskResearching {
		return err
	}
	return lp.askPlanQuestions(ctx, planned)
}

// askPlanQuestions brings the researcher's questions to the owner before
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

// parsePlan reads the researcher's JSON answer, tolerating a fenced block or
// surrounding prose, and bounds what it keeps. Where the researcher can ask
// for design input, a reply that asks needs no plan yet: the researcher plans
// once the input is back.
func parsePlan(reply string, designs bool) (core.Plan, []string, string, error) {
	var in struct {
		Summary    string   `json:"summary"`
		Exists     []string `json:"exists"`
		Changes    []string `json:"changes"`
		OutOfScope []string `json:"out_of_scope"`
		Questions  []string `json:"questions"`
		DependsOn  []string `json:"depends_on"`
		Design     string   `json:"design"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.Plan{}, nil, "", errors.New("the plan was not valid JSON")
	}
	var design string
	if designs {
		design = strings.TrimSpace(in.Design)
	}
	if strings.TrimSpace(in.Summary) == "" && design == "" {
		return core.Plan{}, nil, "", errors.New("the plan had no summary")
	}
	list := func(items []string) []string { return listed(items, 20) }
	return core.Plan{
		Summary:    text.Clip(strings.TrimSpace(in.Summary), 3000),
		Exists:     list(in.Exists),
		Changes:    list(in.Changes),
		OutOfScope: list(in.OutOfScope),
		Questions:  list(in.Questions),
	}, list(in.DependsOn), design, nil
}
