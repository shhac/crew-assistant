package work

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
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
func (lp *Loop) askDesign(ctx context.Context, t core.Task, from, question string, also func(*core.Task), ends ...core.TurnEnd) error {
	return lp.askDesignFor(ctx, t, from, question, "", also, ends...)
}

// askDesignFor pins a researcher's request to the named designer, when given.
func (lp *Loop) askDesignFor(ctx context.Context, t core.Task, from, question, designer string, also func(*core.Task), ends ...core.TurnEnd) error {
	return lp.askDesignAssets(ctx, t, from, question, designer, nil, also, ends...)
}

func (lp *Loop) askDesignAssets(ctx context.Context, t core.Task, from, question, designer string, assets []core.WantedAsset, also func(*core.Task), ends ...core.TurnEnd) error {
	_, err := lp.Core.AskDesign(ctx, t.ID, core.DesignAsk{
		Assets:   assets,
		From:     from,
		For:      designer,
		Question: question,
		Owner: core.DecisionInput{
			Title:          fmt.Sprintf("%s wants more design input on “%s”", from, t.Objective),
			Context:        fmt.Sprintf("%s\n\n%s has already had design input %d times at this step.", question, from, core.DesignLimit),
			Recommendation: "Answer it, or let the team use its judgment",
			Choices:        []string{"Use your judgment", choiceStop},
		},
		Also: also,
	}, ends...)
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
	turn := 0
	if request.Production != nil {
		t, err = lp.Core.StartProductionTurn(ctx, t.ID, request.ID, designer.Name)
		if errors.Is(err, core.ErrConflict) {
			return nil
		}
		if err != nil {
			return err
		}
		request = t.OpenDesign()
		turn = request.Production.Turns[len(request.Production.Turns)-1].N
	}
	base := designerPrompt(p, t, *request, generatesImages(designer)) + learnedGuide(designer, true)
	spec, cleanup, err := lp.roleSpec(t, designer, m.workspace(t), false, m, base, nil)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	defer func() { cleanup() }()
	var retry func(roles.Spec, error) (roles.Spec, error)
	if turn > 0 {
		retry = func(_ roles.Spec, reason error) (roles.Spec, error) {
			// A fresh correction cannot recover exact generation history from
			// an unrecorded group. Drop that group and regenerate in a new turn.
			freshTask, err := lp.Core.StartProductionTurn(ctx, t.ID, request.ID, designer.Name)
			if err != nil {
				return roles.Spec{}, err
			}
			t = freshTask
			request = t.OpenDesign()
			turn = request.Production.Turns[len(request.Production.Turns)-1].N
			prompt := designerPrompt(p, t, *request, generatesImages(designer)) + learnedGuide(designer, true)
			prompt += "\n\nYour previous reply could not be used (" + reason.Error() + "). Its unfinished asset group was discarded. Regenerate the remaining assets with fresh provenance in this new production turn; do not reuse the discarded attachments. Reply with only the JSON object."
			fresh, freshCleanup, err := lp.roleSpec(t, designer, m.workspace(t), false, m, prompt, nil)
			if err != nil {
				return roles.Spec{}, err
			}
			cleanup()
			cleanup = freshCleanup
			return fresh, nil
		}
	}
	var answer designAnswer
	current := 0
	movedOn := false
	reply, learned, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		if turn > 0 && ctx.Err() != nil {
			movedOn = true
			return nil
		}
		if answer, err = parseDesign(reply); err != nil {
			return err
		}
		current, err = designCurrent(answer.Current, answer.Input, t)
		if err == nil && turn > 0 {
			err = lp.Core.ValidateProduction(ctx, t.ID, request.ID, turn, answer.Delivered, answer.Provenance, answer.Escalate != nil)
			if errors.Is(err, core.ErrConflict) || ctx.Err() != nil {
				movedOn = true
				return nil
			}
		}
		return err
	}, retry)
	if err != nil {
		if turn > 0 && (errors.Is(err, core.ErrConflict) || ctx.Err() != nil) {
			return nil
		}
		return lp.roleFailed(ctx, t, designer.Name, err)
	}
	if movedOn {
		return nil
	}
	if turn > 0 && parseErr != nil {
		if errors.Is(parseErr, core.ErrConflict) {
			return nil
		}
		return lp.roleFailed(ctx, t, designer.Name, parseErr)
	}
	lp.recordLearned(ctx, p, t, designer, m, learned)
	// Input that still cannot be read is passed on as written: the role that
	// asked reads it either way.
	if answer.Input == "" && answer.Escalate == nil && turn == 0 {
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
	_, err = lp.Core.RecordDesign(ctx, t.ID, request.ID, core.DesignReply{Designer: designer.Name, Input: answer.Input, Current: current, Escalate: escalate, Turn: turn, Delivered: answer.Delivered, Provenance: answer.Provenance})
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// designAnswer is the designer's reply: its input, which design is current
// after it, and an escalation when the question needs more than design
// input.
type designAnswer struct {
	Delivered  []string              `json:"delivered"`
	Provenance []core.DeliveredAsset `json:"provenance"`
	Input      string                `json:"input"`
	Current    string                `json:"current"`
	Escalate   *escalation           `json:"escalate"`
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
	if in.Delivered != nil || in.Provenance != nil {
		if err := core.ValidateAssetProvenance(in.Delivered, in.Provenance); err != nil {
			return designAnswer{}, err
		}
	}
	if e := in.Escalate; e != nil {
		e.Evidence, e.Consequences, e.Recommendation = text.Clip(strings.TrimSpace(e.Evidence), 2000), text.Clip(strings.TrimSpace(e.Consequences), 2000), text.Clip(strings.TrimSpace(e.Recommendation), 1000)
		e.Alternatives = listed(e.Alternatives, 6)
		if e.Evidence == "" && len(e.Alternatives) == 0 && e.Consequences == "" && e.Recommendation == "" {
			in.Escalate = nil
		} else if e.Recommendation == "" {
			return designAnswer{}, errors.New("the escalation had no recommendation")
		}
	}
	if in.Input == "" && in.Escalate == nil && in.Delivered == nil {
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
	if r.Production == nil {
		b.WriteString(`
Give design input only: read what you need, change nothing, and do not commit, deliver, land or approve anything. Answer the question so the one who asked can go on, and say what you would choose and why.
Generating and attaching images is allowed as design input; it writes nothing to the repository.
While you work, attach_file keeps a mockup with your input: an SVG, HTML or Markdown sketch you write out as its content, or an image or other file already in the current directory, named by its path. Everyone who works on the task afterwards can open it.
`)
	} else {
		b.WriteString(productionPrompt(*r.Production))
	}
	if generates {
		b.WriteString("When the work needs new illustrations (characters, props, scene art), first look for the project's existing illustrated assets in the same area. Generate raster art with your image generation tool matching their style, format (such as .webp or .png) and scale. Attach it with attach_file using generated for the image's file name, within the same limits. Hand-written SVG is for mockups, icons, diagrams, simple shapes, or when image generation is unavailable or fails. Say in your input why you chose SVG.\n")
	} else {
		b.WriteString("Image generation isn't available to you here. Draw vector illustrations (SVG), and where the work moves (characters, ambient motion), use animated SVG or CSS. Look first for the project's existing illustrated assets in the same area, matching their style, format and scale.\n")
	}
	b.WriteString(imageGenerators(t.Roles) + "\n")
	if !generates {
		if names := generatingMembers(t.Roles); len(names) > 0 {
			b.WriteString("If the project style calls for raster art, say so in your input and recommend " + strings.Join(names, ", ") + " to generate it.\n")
		} else {
			b.WriteString("No member on this team can generate raster art.\n")
		}
	}
	b.WriteString(`If answering well needs more than design input, such as a choice only the owner can make or work beyond this task, escalate instead: give the evidence, the alternatives, the consequences of each and your recommendation. The owner decides, and the task goes back to whoever asked.
`)
	if r.Production != nil {
		b.WriteString(`Reply with only JSON: {"input":"handover notes", "current":"", "delivered":["asset name"], "provenance":[{"asset":"asset name", "prompt":"exact generation prompt", "generator":"tool and model", "settings":{}, "references":[]}], "escalate":null}. Give one provenance entry per delivered asset, including all settings and references (with hashes where available). Use an empty references list when none were used. The daemon computes the asset's SHA-256 and records the producing turn. To escalate use "escalate":{"evidence":"...","alternatives":["..."],"consequences":"...","recommendation":"..."}. To abandon an unfinished group, return empty delivered and provenance arrays together with a non-null escalation object containing your recommendation; its uncompleted attachments will be discarded. Empty arrays with escalate:null do not abandon attached assets: every attachment must have delivered provenance.`)
		return b.String()
	}
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
	var b, superseded, advice, production strings.Builder
	current, hasCurrent := t.CurrentDesignInput()
	if hasCurrent {
		fmt.Fprintf(&b, "\nThe current design, design %d, is the target: build to it and judge against it.\n", current.N)
		b.WriteString(designEntry(t, current))
	}
	for _, r := range t.Design {
		switch {
		case (r.Input == "" && r.Production == nil) || (hasCurrent && r.ID == current.ID):
		case r.Marked:
			superseded.WriteString(designEntry(t, r))
		case r.Production != nil:
			production.WriteString(designEntry(t, r))
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
	if production.Len() > 0 {
		b.WriteString("\nProduction assets on this task, to build from once handed over:\n" + production.String())
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
	if r.Production != nil {
		b.WriteString(productionSummary(*r.Production))
	}
	var names []string
	for _, a := range t.Attachments {
		if a.Design == r.ID {
			name := a.Name
			if a.Made != "" {
				name += " (" + a.Made + ")"
			}
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		fmt.Fprintf(&b, "  Attached: %s (where to open them is under “Files attached to this task” in your instructions)\n", strings.Join(names, ", "))
	}
	return b.String()
}

const visualDesignWork = "new visual assets or visual design (icons, illustrations, images, significant layout or styling)"

// designGuide tells a role at a step how to hand the task to the designer,
// or that it has had all the design input this step allows.
func designGuide(t core.Task, asker core.Role, step string, how string) string {
	if !designsFor(t, asker) {
		return ""
	}
	designer, _ := t.Designer()
	abilities := "\n" + imageGenerators(t.Roles)
	if step == core.TaskWriting {
		abilities += "\nFor finished assets, ask with only a ```production block instead of ```design: one named asset per line (name: what it is), at most 100, with optional notes after a blank line. Changes in that turn are set aside. The designer generates and attaches complete groups of at most 10, returning only after all are ready. You receive readable attachments, provenance.json (prompts, generator, settings, references, hashes and producing turns), and a rejected-variants.zip archive. Build from those files, copying them into the workspace as the task needs."
	}
	if names := generatingDesigners(t); len(names) > 0 && step == core.TaskResearching && len(t.RolesOf(core.RoleDesigner)) > 1 {
		abilities += "\nWhen work needs raster illustrations, name a designer who can generate images: " + strings.Join(names, ", ") + "."
	} else if len(generatingDesigners(t)) == 0 {
		note := "your reply"
		if step == core.TaskResearching {
			note = "the plan questions"
		}
		abilities += "\nNo designer on this team can generate raster art; if it is needed, note that in " + note + "."
	}
	if n := t.DesignsAt(step); n >= core.DesignLimit {
		return abilities + fmt.Sprintf("\n\nYou have had design input from %s %d times at this step, the most it allows. Go on with what you have; asking again brings the question to the owner instead.", designer.Name, n)
	}
	if step == core.TaskResearching && len(t.RolesOf(core.RoleDesigner)) > 1 {
		var names []string
		for _, seat := range t.RolesOf(core.RoleDesigner) {
			names = append(names, seat.Name)
		}
		return abilities + fmt.Sprintf("\n\nThe team's designers are %s. Work needing %s must go to a designer for design input before you plan or build it as final; do not draw or invent those assets yourself. Name the designer in your design reply to send it only to that seat; leave designer empty to ask any designer. For that work, or if you need other design input before you can go on well, %s The task comes back to you with the answer.", strings.Join(names, ", "), visualDesignWork, how)
	}
	return abilities + fmt.Sprintf("\n\n%s is the team's designer. Work needing %s must go to %s for design input before you plan or build it as final; do not draw or invent those assets yourself. For that work, or if you need other design input before you can go on well, %s The task goes to %s and comes back to you with the answer.", designer.Name, visualDesignWork, designer.Name, how, designer.Name)
}
