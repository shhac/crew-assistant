package work

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
// never finished. designer is the seat that claimed the step, if the team
// has one.
func (lp *Loop) design(ctx context.Context, p core.Project, t core.Task, m medium, designer core.Role) error {
	request := t.OpenDesign()
	if designer.Name == "" || request == nil {
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
	base := designerPrompt(p, t, *request, generatesImages(designer)) + learnedGuide(designer, true)
	spec, cleanup, err := lp.roleSpec(t, designer, m.workspace(t), false, m, base)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer cleanup()
	var answer designAnswer
	current := 0
	reply, learned, _, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		if answer, err = parseDesign(reply); err != nil {
			return err
		}
		current, err = designCurrent(answer.Current, answer.Input, t)
		return err
	})
	if err != nil {
		return lp.roleFailed(ctx, t, designer.Name, err)
	}
	lp.recordLearned(ctx, p, t, designer, m, learned)
	// Input that still cannot be read is passed on as written: the role that
	// asked reads it either way.
	if answer.Input == "" && answer.Escalate == nil {
		answer.Input, current = text.Clip(strings.TrimSpace(reply), 6000), 0
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
	_, err = lp.Core.RecordDesign(ctx, t.ID, request.ID, core.DesignReply{Designer: designer.Name, Input: answer.Input, Current: current, Escalate: escalate})
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// designAnswer is the designer's reply: its input, which design is current
// after it, and an escalation when the question needs more than design
// input.
type designAnswer struct {
	Input    string      `json:"input"`
	Current  string      `json:"current"`
	Escalate *escalation `json:"escalate"`
}

var designNumber = regexp.MustCompile(`^(?:design\s*)?#?(\d+)$`)

// designCurrent reads which design the designer says is current after its
// answer: "this" for its own input, "design N" to keep or restore an
// earlier one, or nothing, for input that is advice only.
func designCurrent(said, input string, t core.Task) (int, error) {
	said = strings.ToLower(strings.TrimSpace(said))
	switch said {
	case "", "none", "advice":
		return 0, nil
	case "this":
		if input == "" {
			return 0, errors.New(`"current": "this" needs design input to make current`)
		}
		return core.CurrentThis, nil
	}
	m := designNumber.FindStringSubmatch(said)
	if m == nil {
		return 0, fmt.Errorf(`"current" is "this", "design N" or empty, not %q`, said)
	}
	n, _ := strconv.Atoi(m[1])
	if t.DesignNumbered(n) == nil {
		return 0, fmt.Errorf(`there is no design %d; name one listed above, or use "this"`, n)
	}
	return n, nil
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
// reads, changes nothing and gains no other authority. generates is whether
// its engine can generate images.
func designerPrompt(p core.Project, t core.Task, r core.DesignRequest, generates bool) string {
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
While you work, attach_file keeps a mockup with your input: an SVG, HTML or Markdown sketch you write out as its content, or an image or other file already in the current directory, named by its path. Everyone who works on the task afterwards can open it.
`)
	if generates {
		b.WriteString("When the task needs a raster image, such as an icon, an illustration or a mockup, you can make one with your image generation tool, then attach it with attach_file by giving the generated image's file name as generated, within the same limits. If image generation isn't available to you, say so in your input and sketch in SVG, HTML or Markdown instead.\n")
	} else {
		b.WriteString("Image generation isn't available to you here, so where the task needs an icon, an illustration or a mockup, sketch it in SVG, HTML or Markdown instead.\n")
	}
	b.WriteString(`If answering well needs more than design input, such as a choice only the owner can make or work beyond this task, escalate instead: give the evidence, the alternatives, the consequences of each and your recommendation. The owner decides, and the task goes back to whoever asked.
`)
	b.WriteString("\nSay which design is current after your answer: the one the implementer builds to and every checker judges against. \"this\" makes your input the current design; \"design N\" keeps or brings back an earlier design by its number; empty means your input is advice only, and the current design stays as it is. " + currentLine(t) + "\n")
	b.WriteString(`
Reply with only this JSON object:
{"input": "your design input", "current": "this", "escalate": null}
or, to escalate:
{"input": "what you can say now, or empty", "current": "", "escalate": {"evidence": "...", "alternatives": ["..."], "consequences": "...", "recommendation": "..."}}`)
	return b.String()
}

// currentLine says which design is current, for the designer deciding
// whether its answer changes that.
func currentLine(t core.Task) string {
	if current, ok := t.CurrentDesignInput(); ok {
		return fmt.Sprintf("The current design is design %d.", current.N)
	}
	return "There is no current design yet."
}

// stepWord names the role that works at a step.
func stepWord(step string) string {
	if step == core.TaskResearching {
		return "researcher"
	}
	return "implementer"
}

// designText is the design input already given on the task, as everyone
// who works on it afterwards reads it: the current design first, as the
// target, then superseded designs, which are not, then advice.
func designText(t core.Task) string {
	var b, superseded, advice strings.Builder
	current, hasCurrent := t.CurrentDesignInput()
	if hasCurrent {
		fmt.Fprintf(&b, "\nThe current design, design %d, is the target: build to it and judge against it.\n", current.N)
		b.WriteString(designEntry(t, current))
	}
	for _, r := range t.Design {
		switch {
		case r.Input == "" || (hasCurrent && r.ID == current.ID):
		case r.Marked:
			superseded.WriteString(designEntry(t, r))
		default:
			advice.WriteString(designEntry(t, r))
		}
	}
	if superseded.Len() > 0 {
		b.WriteString("\nSuperseded designs: not current and not the target, kept only as history:\n" + superseded.String())
	}
	if advice.Len() > 0 {
		b.WriteString("\nDesign advice given on this task, not a design to build to:\n" + advice.String())
	}
	return b.String()
}

// designEntry is one input from the designer: what was asked, what came
// back, and the files attached to it.
func designEntry(t core.Task, r core.DesignRequest) string {
	var b strings.Builder
	b.WriteString("- ")
	if r.N > 0 {
		fmt.Fprintf(&b, "Design %d. ", r.N)
	}
	fmt.Fprintf(&b, "%s asked: %s\n  %s answered: %s\n", r.From, text.Clip(r.Question, 500), r.Designer, r.Input)
	var names []string
	for _, a := range t.Attachments {
		if a.Design == r.ID {
			names = append(names, a.Name)
		}
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "  Attached: %s (where to open them is under “Files attached to this task” in your instructions)\n", strings.Join(names, ", "))
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
