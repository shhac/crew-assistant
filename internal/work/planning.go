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
// researcher is the seat that claimed the step, if the team has one.
func (lp *Loop) researchTask(ctx context.Context, p core.Project, t core.Task, m medium, researcher core.Role) error {
	if researcher.Name == "" {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	// A plan recorded before a restart is not paid for twice. A plan whose
	// questions were answered, or one made before a checker asked for more
	// research, is worked out again.
	if planned(t) {
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
	base := researcherPrompt(p, t, otherWork(snap, t), snap.Projects) + learnedGuide(researcher, true) + handOnGuide(t, core.RoleResearcher, "")
	spec, cleanup, err := lp.roleSpec(t, researcher, m.workspace(t), false, m, base, nil)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	var plan core.Plan
	var end core.TurnEnd
	var dependsOn []string
	var design, designer string
	attempts := 0
	inconsistent := false
	_, hasDesigner := t.Designer()
	reply, learned, _, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		attempts++
		clean, block := splitHandOn(reply)
		plan, dependsOn, design, designer, err = parsePlan(clean, designsFor(t, researcher), !hasDesigner, t.RolesOf(core.RoleDesigner))
		end.HandOnWhy, end.Problems = parseHandOn(block)
		if err == nil && design == "" && core.UndeclaredPlanWait(plan, core.PlanDependencies(snap, t, dependsOn), t) {
			inconsistent = true
			if attempts == 1 {
				return errors.New("the summary says implementation waits, but declares no depends_on or prerequisites; name the task or external condition it waits for, or reword the summary if implementation does not actually wait")
			}
			plan.Questions = append(plan.Questions, core.UndeclaredWaitQuestion)
		}
		return err
	})
	if err != nil {
		return lp.roleFailed(ctx, t, researcher.Name, err)
	}
	lp.recordLearned(ctx, p, t, researcher, m, learned)
	if design != "" {
		return lp.askDesignFor(ctx, t, researcher.Name, design, designer, nil, end)
	}
	// A plan that still cannot be read is kept as written rather than stopping
	// the work: the implementer reads it either way.
	if plan.Summary == "" {
		clean, _ := splitHandOn(reply)
		plan, dependsOn = core.Plan{Summary: text.Clip(strings.TrimSpace(clean), 3000)}, nil
		if inconsistent || core.UndeclaredPlanWait(plan, core.PlanDependencies(snap, t, dependsOn), t) {
			plan.Questions = append(plan.Questions, core.UndeclaredWaitQuestion)
		}
	}
	var checks []string
	for _, quoted := range plan.OwnerChecks {
		if c := matchCriterion(t.Criteria, quoted); c != "" {
			checks = append(checks, c)
		}
	}
	plan.OwnerChecks = checks
	plan.Role = researcher.Name
	var prerequisites []string
	for _, pre := range plan.Prerequisites {
		prerequisites = append(prerequisites, pre.What)
	}
	planned, err := lp.Core.RecordPlan(ctx, t.ID, plan, dependsOn, prerequisites, end)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	if err != nil || planned.Status != core.TaskResearching {
		return err
	}
	return lp.askPlanQuestions(ctx, planned)
}

// planned reports a task whose researcher has already done what it is
// researching for now.
func planned(t core.Task) bool {
	if t.Plan == nil || t.Plan.Answered {
		return false
	}
	request := t.OpenResearch()
	return request == nil || !t.Plan.At.Before(request.At)
}

// askPlanQuestions brings the researcher's questions to the owner, and
// their answer comes back to the researcher.
func (lp *Loop) askPlanQuestions(ctx context.Context, t core.Task) error {
	if t.Plan == nil || len(t.Plan.Questions) == 0 {
		return lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	title := fmt.Sprintf("%s has questions about “%s” before starting", t.Plan.Role, t.Objective)
	if len(t.Revisions) > 0 {
		title = fmt.Sprintf("%s has questions about “%s”", t.Plan.Role, t.Objective)
	}
	_, err := lp.Core.AskQuestion(ctx, t.ID, core.Asker{From: t.Plan.Role, Step: core.TaskResearching}, core.DecisionInput{
		Title:          title,
		Context:        strings.TrimSpace(numbered(t.Plan.Questions)),
		Recommendation: "Answer what you can, or let the team use its judgment",
		Choices:        []string{"Use your judgment", choiceStop},
	})
	return err
}

// otherWork is the owner's other unfinished tasks, which a plan may say
// this one has to wait for.
func otherWork(snap core.Snapshot, t core.Task) []core.Task {
	var out []core.Task
	for _, other := range snap.Tasks {
		if other.ID != t.ID && !other.Finished() {
			out = append(out, other)
		}
	}
	return out
}

// A plan item is kept whole up to maxPlanItem, since a clipped step loses
// the detail the implementer and the reviewers work from. The lists
// together stay within maxPlanLists, so a runaway reply can't bloat the
// task's record and every prompt that carries the plan.
const (
	maxPlanItems = 20
	maxPlanItem  = 3000
	maxPlanLists = 24000
)

// parsePlan reads the researcher's JSON answer, tolerating a fenced block or
// surrounding prose, and bounds what it keeps. Where the researcher can ask
// for design input, a reply that asks needs no plan yet: the researcher plans
// once the input is back.
func parsePlan(reply string, designs, noDesigner bool, seats []core.Role) (core.Plan, []string, string, string, error) {
	var in struct {
		OwnerChecks   []string `json:"owner_checks"`
		NeedsDesigner string   `json:"needs_designer"`
		Summary       string   `json:"summary"`
		Exists        []string `json:"exists"`
		Changes       []string `json:"changes"`
		FailurePaths  []string `json:"failure_paths"`
		Tests         []string `json:"tests"`
		OutOfScope    []string `json:"out_of_scope"`
		Questions     []string `json:"questions"`
		DependsOn     []string `json:"depends_on"`
		Prerequisites []string `json:"prerequisites"`
		SplitOff      []struct {
			Title        string   `json:"title"`
			Requirements []string `json:"requirements"`
		} `json:"split_off"`
		Design   string `json:"design"`
		Designer string `json:"designer"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.Plan{}, nil, "", "", errors.New("the plan was not valid JSON")
	}
	var design string
	if designs {
		design = strings.TrimSpace(in.Design)
	}
	designer := strings.TrimSpace(in.Designer)
	if design != "" && designer != "" {
		valid := false
		for _, seat := range seats {
			if seat.Name == designer {
				valid = true
			}
		}
		if !valid {
			return core.Plan{}, nil, "", "", fmt.Errorf("designer %q is not a designer seat on this task", designer)
		}
	} else {
		designer = ""
	}
	if strings.TrimSpace(in.Summary) == "" && design == "" {
		return core.Plan{}, nil, "", "", errors.New("the plan had no summary")
	}
	// What will change is kept first, so a plan over budget loses its least
	// needed lists instead.
	left := maxPlanLists
	keep := func(items []string) []string {
		var out []string
		for _, item := range items {
			if item = strings.TrimSpace(item); item == "" || len(out) == maxPlanItems || left <= 0 {
				continue
			}
			item = text.Clip(item, min(maxPlanItem, left))
			left -= len(item)
			out = append(out, item)
		}
		return out
	}
	plan := core.Plan{Summary: text.Clip(strings.TrimSpace(in.Summary), 3000)}
	if noDesigner {
		plan.NeedsDesigner = text.Clip(strings.TrimSpace(in.NeedsDesigner), maxPlanItem)
	}
	plan.OwnerChecks = keep(in.OwnerChecks)
	plan.Changes = keep(in.Changes)
	plan.FailurePaths = keep(in.FailurePaths)
	plan.Tests = keep(in.Tests)
	plan.Exists = keep(in.Exists)
	plan.OutOfScope = keep(in.OutOfScope)
	plan.Questions = asked(in.Questions, maxPlanItems)
	for _, part := range in.SplitOff {
		if title := strings.TrimSpace(part.Title); title != "" && len(plan.SplitOff) < core.MaxSplitOff {
			plan.SplitOff = append(plan.SplitOff, core.SplitPart{Objective: text.Clip(title, 500), Criteria: listed(part.Requirements, maxPlanItems)})
		}
	}
	for _, what := range listed(in.Prerequisites, maxPlanItems) {
		plan.Prerequisites = append(plan.Prerequisites, core.Prerequisite{What: text.Clip(what, 300)})
	}
	return plan, listed(in.DependsOn, maxPlanItems), design, designer, nil
}
