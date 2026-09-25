package work

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// designsFor reports whether a role can hand its task to a designer: the
// team has one, in a seat of its own.
func designsFor(t core.Task, asker core.Role) bool {
	designer, ok := t.Designer()
	return ok && designer.Name != asker.Name
}

// askDesign hands the task to the designer with a role's question, or past
// the limit brings the question to the owner. also changes the task in the
// same change. A task that moved on meanwhile, such as one stopped, is left
// as it is.
func (lp *Loop) askDesign(ctx context.Context, t core.Task, from, question string, also func(*core.Task)) error {
	_, err := lp.Core.AskDesign(ctx, t.ID, core.DesignAsk{
		From:     from,
		Question: question,
		Owner: core.DecisionInput{
			Title:          fmt.Sprintf("%s wants more design input on “%s”", from, t.Objective),
			Context:        fmt.Sprintf("%s\n\n%s has already had design input %d times at this step.", question, from, core.DesignLimit),
			Recommendation: "Answer it, or let the team use its judgment",
			Choices:        []string{"Use your judgment", choiceStop},
		},
		Also: also,
	})
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// design runs the designer on the question it was handed. The designer only
// reads, starts afresh every time, and its input is recorded in the same
// change that hands the task back, so a restart runs it again only if it
// never finished.
func (lp *Loop) design(ctx context.Context, p core.Project, t core.Task, m medium) error {
	designer, ok := t.Designer()
	request := t.OpenDesign()
	if !ok || request == nil {
		// Nothing to ask the designer: go back to whichever step can go on.
		back := core.TaskWriting
		if _, researches := t.Researcher(); researches && t.Plan == nil {
			back = core.TaskResearching
		}
		return lp.setStatus(ctx, t.ID, back, "")
	}
	if held, err := lp.holdForUsage(ctx, t, designer); held || err != nil {
		return err
	}
	t, err := lp.prepareWorkspace(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	base := designerPrompt(p, t, *request) + learnedGuide(designer, true)
	spec, cleanup, err := lp.roleSpec(t, designer, m.workspace(), false, m, base)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	var answer designAnswer
	var reply string
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return lp.roleFailed(ctx, t, designer.Name, err)
		}
		var learned string
		reply, learned = splitBlock(result.Text, "learned")
		lp.recordLearned(ctx, p, t, designer, m, learned)
		if answer, err = parseDesign(reply); err == nil {
			break
		}
		spec.Prompt = retryPrompt(base, err)
	}
	// Input that still cannot be read is passed on as written: the role that
	// asked reads it either way.
	if answer.Input == "" && answer.Escalate == nil {
		answer.Input = text.Clip(strings.TrimSpace(reply), 6000)
	}
	var escalate *core.DecisionInput
	if e := answer.Escalate; e != nil {
		escalate = &core.DecisionInput{
			Title:          fmt.Sprintf("%s needs your call on the design of “%s”", designer.Name, t.Objective),
			Context:        escalationText(*request, *e),
			Recommendation: e.Recommendation,
			Choices:        []string{"Use your judgment", choiceStop},
		}
	}
	_, err = lp.Core.RecordDesign(ctx, t.ID, request.ID, designer.Name, answer.Input, escalate)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// designAnswer is the designer's reply: its input, and an escalation when
// the question needs more than design input.
type designAnswer struct {
	Input    string      `json:"input"`
	Escalate *escalation `json:"escalate"`
}

type escalation struct {
	Evidence       string   `json:"evidence"`
	Alternatives   []string `json:"alternatives"`
	Consequences   string   `json:"consequences"`
	Recommendation string   `json:"recommendation"`
}

// parseDesign reads the designer's JSON answer. An escalation must say what
// it recommends, since that is what the owner is asked to weigh.
func parseDesign(reply string) (designAnswer, error) {
	var in designAnswer
	if err := decodeReply(reply, &in); err != nil {
		return designAnswer{}, errors.New("the design input was not valid JSON")
	}
	in.Input = text.Clip(strings.TrimSpace(in.Input), 6000)
	if e := in.Escalate; e != nil {
		e.Evidence, e.Consequences, e.Recommendation = text.Clip(strings.TrimSpace(e.Evidence), 2000), text.Clip(strings.TrimSpace(e.Consequences), 2000), text.Clip(strings.TrimSpace(e.Recommendation), 1000)
		e.Alternatives = listed(e.Alternatives, 6)
		if e.Evidence == "" && len(e.Alternatives) == 0 && e.Consequences == "" && e.Recommendation == "" {
			in.Escalate = nil
		} else if e.Recommendation == "" {
			return designAnswer{}, errors.New("the escalation had no recommendation")
		}
	}
	if in.Input == "" && in.Escalate == nil {
		return designAnswer{}, errors.New("the design input was empty")
	}
	return in, nil
}

// escalationText is what the owner reads about a question the designer
// could not settle with design input alone.
func escalationText(r core.DesignRequest, e escalation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s asked: %s\n", r.From, r.Question)
	if e.Evidence != "" {
		fmt.Fprintf(&b, "\nEvidence: %s\n", e.Evidence)
	}
	if len(e.Alternatives) > 0 {
		b.WriteString("\nAlternatives:\n" + numbered(e.Alternatives))
	}
	if e.Consequences != "" {
		fmt.Fprintf(&b, "\nConsequences: %s\n", e.Consequences)
	}
	return strings.TrimSpace(b.String())
}

// designerPrompt asks the designer for design input on one question. It
// reads, changes nothing and gains no other authority.
func designerPrompt(p core.Project, t core.Task, r core.DesignRequest) string {
	var b strings.Builder
	b.WriteString(briefText(p, t))
	b.WriteString(planText(t))
	switch {
	case isCode(p, t) && len(t.Revisions) > 0:
		fmt.Fprintf(&b, "\nYou are in a clone of the repository, at draft %d of this task. %s\n", len(t.Revisions), repoInstructions)
	case isCode(p, t):
		b.WriteString("\nYou are in a clone of the repository, on the branch this task will be written on. " + repoInstructions + "\n")
	case len(t.Revisions) > 0:
		fmt.Fprintf(&b, "\nThe current directory holds draft %d of this task.\n", len(t.Revisions))
	default:
		b.WriteString("\nThe current directory is where this task's draft will be written.\n")
	}
	b.WriteString(designText(t))
	fmt.Fprintf(&b, "\n%s, the task's %s, asks for your design input:\n%s\n", r.From, stepWord(r.Step), r.Question)
	b.WriteString(`
Give design input only: read what you need, change nothing, and do not commit, deliver, land or approve anything. Answer the question so the one who asked can go on, and say what you would choose and why.
If answering well needs more than design input, such as a choice only the owner can make or work beyond this task, escalate instead: give the evidence, the alternatives, the consequences of each and your recommendation. The owner decides, and the task goes back to whoever asked.

Reply with only this JSON object:
{"input": "your design input", "escalate": null}
or, to escalate:
{"input": "what you can say now, or empty", "escalate": {"evidence": "...", "alternatives": ["..."], "consequences": "...", "recommendation": "..."}}`)
	return b.String()
}

// stepWord names the role that works at a step.
func stepWord(step string) string {
	if step == core.TaskResearching {
		return "researcher"
	}
	return "implementer"
}

// designText is the design input already given on the task, as everyone
// who works on it afterwards reads it.
func designText(t core.Task) string {
	var b strings.Builder
	for _, r := range t.Design {
		if r.Input == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\nDesign input given on this task so far:\n")
		}
		fmt.Fprintf(&b, "- %s asked: %s\n  %s answered: %s\n", r.From, text.Clip(r.Question, 500), r.Designer, r.Input)
	}
	return b.String()
}

// designGuide tells a role at a step how to hand the task to the designer,
// or that it has had all the design input this step allows.
func designGuide(t core.Task, asker core.Role, step string, how string) string {
	if !designsFor(t, asker) {
		return ""
	}
	designer, _ := t.Designer()
	if n := t.DesignsAt(step); n >= core.DesignLimit {
		return fmt.Sprintf("\n\nYou have had design input from %s %d times at this step, the most it allows. Go on with what you have; asking again brings the question to the owner instead.", designer.Name, n)
	}
	return fmt.Sprintf("\n\n%s is the team's designer. If you need design input before you can go on well, %s The task goes to %s and comes back to you with the answer.", designer.Name, how, designer.Name)
}
