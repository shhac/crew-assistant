package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/text"
)

// maxPMLandingFailures is how many landings the PM approved may fail, each
// sent back to the implementer, before the owner decides instead.
const maxPMLandingFailures = 2

// pmDecides reports whether the team's PM, not the owner, decides whether
// the task's change lands: the project's current policy says so, the task
// lands by push, and the team has a PM. The policy is read from the project,
// not the task's pinned copy, so the owner taking the decision back applies
// to work already under way.
func pmDecides(p core.Project, t core.Task) bool {
	if p.Playbook == nil || !p.Playbook.Land.ByPM() || taskPlaybook(p, t).Land.Way() != core.LandPush {
		return false
	}
	_, ok := p.PMSeat()
	return ok
}

// asksFirst reports whether the owner or the PM approves the task's change
// before it lands.
func asksFirst(p core.Project, t core.Task) bool {
	return pmDecides(p, t) || taskPlaybook(p, t).Land.AsksFirst()
}

// pmApproved reports whether the approval the task's change lands on is the
// PM's.
func pmApproved(t core.Task) bool {
	return t.LandDecision != nil && t.LandDecision.Land && t.Approved == t.LandDecision.Revision && approvalStands(t)
}

// approvalHolds reports whether the task's approval still lets it land: it
// stands, and if the PM gave it, the PM still decides.
func approvalHolds(p core.Project, t core.Task) bool {
	return approvalStands(t) && (!pmApproved(t) || pmDecides(p, t))
}

// pmLanding asks the team's PM whether a change every checker passed lands.
// Only a signed-off change is put to it; anything else, a PM that cannot
// answer, and repeated failed landings come to the owner instead.
func (lp *Loop) pmLanding(ctx context.Context, p core.Project, t core.Task, r core.Revision, m medium) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	if fresh, ok := findTask(snap, p.ID, t.ID); ok {
		t = fresh
	}
	if failed := t.LandingFailures; len(failed) >= maxPMLandingFailures || (len(failed) > 0 && t.Round >= t.MaxRounds) {
		return lp.askOwnerToLand(ctx, p, t, r, m, core.DecisionInput{
			Title:          fmt.Sprintf("“%s” failed to land %s with the PM's approval", t.Objective, times(len(failed))),
			Context:        "Each time it went back to the implementer:\n" + numbered(failed),
			Recommendation: "Approve to land it yourself if the latest draft looks right; otherwise request changes and say what should be different",
		})
	}
	if why := core.SignedOff(snap, t); len(why) > 0 {
		return lp.askOwnerToLand(ctx, p, t, r, m, core.DecisionInput{Context: "The PM can't land it yet: " + strings.Join(why, "; ") + "."})
	}
	seat, _ := p.PMSeat()
	if held, err := lp.holdForUsage(ctx, t, seat); held || err != nil {
		return err
	}
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Like its look at the list, the PM reads only what its prompt carries.
	spec := lp.baseSpec(seat, dir, pmLandingPrompt(snap, p, t, r))
	lp.withTools(&spec, lp.projectTools(p.ID))
	var decided core.LandDecision
	_, _, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		decided, err = parsePMLanding(reply)
		return err
	})
	if why := errors.Join(err, parseErr); why != nil {
		return lp.askOwnerToLand(ctx, p, t, r, m, core.DecisionInput{Context: fmt.Sprintf("%s couldn't decide whether it lands: %s.", seat.Name, text.Clip(why.Error(), 300))})
	}
	decided.Revision = r.N
	return lp.pmDecided(ctx, t, r, m, seat, decided)
}

// pmDecided records what the PM decided. A land that the task or the policy
// moved out from under is not an error: the next step looks again.
func (lp *Loop) pmDecided(ctx context.Context, t core.Task, r core.Revision, m medium, seat core.Role, decided core.LandDecision) error {
	hold := core.DecisionInput{}
	if !decided.Land {
		hold = core.DecisionInput{
			Title:          fmt.Sprintf("%s held “%s”", seat.Name, t.Objective),
			Context:        fmt.Sprintf("%s held it: %s\n\n%s\n\n%s", seat.Name, decided.Reason, text.Clip(r.Summary, 600), m.deliveryNote(t)),
			Recommendation: "Leave it with the PM, which looks again once more work lands here; or approve to land it yourself",
			Choices:        []string{choiceApprove, choiceChanges},
		}
	}
	_, err := lp.Core.DecideLanding(ctx, t.ID, decided, hold)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// askOwnerToLand brings the owner the approval the PM could not give. in may
// set the title and recommendation, and its context comes first.
func (lp *Loop) askOwnerToLand(ctx context.Context, p core.Project, t core.Task, r core.Revision, m medium, in core.DecisionInput) error {
	if in.Title == "" {
		in.Title = approvalTitle(t, taskPlaybook(p, t))
	}
	if in.Recommendation == "" {
		in.Recommendation = choiceApprove
	}
	in.Context = strings.TrimSpace(in.Context + "\n\n" + text.Clip(r.Summary, 600) + "\n\n" + m.deliveryNote(t))
	in.Choices = []string{choiceApprove, choiceChanges}
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionDelivery, in)
	return err
}

// keepsCommits reports whether the PM landed the change keeping the task's
// own commits, rather than as one commit.
func keepsCommits(t core.Task) bool {
	return pmApproved(t) && t.LandDecision.Method == core.LandKeepCommits
}

// cleanUp removes the task branch of a change the PM landed from the clone,
// whichever way it landed. The landing has happened by then, so a failure to
// clean up is only noted; it never undoes the landing.
func (lp *Loop) cleanUp(ctx context.Context, t core.Task, r core.Revision, m medium, landed error) error {
	if landed != nil || !pmApproved(t) || t.Branch == "" {
		return landed
	}
	if gm, ok := m.(gitMedium); ok {
		if err := gm.repo.DropBranch(ctx, t.Branch, r.Ref); err != nil {
			lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_cleanup", ProjectID: t.ProjectID}, err)
		}
	}
	return nil
}

// landingFailure notes why a landing the PM approved failed, on a task
// going back to the implementer. The PM's approval goes with it.
func landingFailure(t *core.Task, why string) {
	if !pmApproved(*t) {
		return
	}
	t.LandingFailures = append(t.LandingFailures, text.Clip(why, 400))
	t.LandDecision = nil
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// pmLandingPrompt is what the PM needs to decide whether a signed-off
// change lands now: the change and its checks, what it depends on, and the
// rest of the project's unfinished work.
func pmLandingPrompt(snap core.Snapshot, p core.Project, t core.Task, r core.Revision) string {
	var b strings.Builder
	target := taskPlaybook(p, t).Land.Target
	fmt.Fprintf(&b, "You are the PM for the project %s. Goal: %s\n", p.Title, p.Brief.Goal)
	fmt.Fprintf(&b, `
Decide whether this change lands on %s now, or is held. Every reviewer and QA passed it and nothing waits on the owner. Landing catches it up with %s, has QA check the merged result and moves %s forward; nothing is ever forced. Hold it when it should wait, for example until another change lands first. Only decide: do not change anything, and do not direct the team.

If it lands, choose how. "squash" lands it as one commit worded from the request, leaving the team's %d drafts and catch-up merges behind; it suits most changes. "fast-forward" moves %s onto the task's own commits as they are; choose it only when those commits are each worth keeping in the history. Either way the task's branch is cleaned up after.
`, target, target, target, r.N, target)
	fmt.Fprintf(&b, "\nThe change: %s (%s)\n", text.Clip(t.Objective, 300), t.ID)
	for _, c := range t.Criteria {
		fmt.Fprintf(&b, "- criterion: %s\n", text.Clip(c, 300))
	}
	fmt.Fprintf(&b, "Draft %d: %s\n", r.N, text.Clip(r.Summary, 800))
	for _, v := range t.Verdicts {
		if v.Revision == r.N {
			fmt.Fprintf(&b, "- %s: %s\n", v.Role, text.Clip(v.Summary, 300))
		}
	}
	for _, id := range t.DependsOn {
		if dep, ok := findTask(snap, p.ID, id); ok {
			fmt.Fprintf(&b, "It depends on: %s (%s)\n", text.Clip(dep.Objective, 300), dep.Status)
		}
	}
	b.WriteString("\nThe project's other unfinished tasks:\n")
	for _, other := range snap.Tasks {
		if other.ProjectID != p.ID || other.ID == t.ID || other.Finished() {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", other.ID, other.Status, text.Clip(other.Objective, 300))
		if len(other.DependsOn) > 0 {
			fmt.Fprintf(&b, "  waits for: %s\n", waitsLine(other))
		}
	}
	if p.PMDirection != "" {
		fmt.Fprintf(&b, "\nThe owner told you: %s\n", p.PMDirection)
	}
	b.WriteString(`
Reply with only this JSON object:
{"land": true or false, "how": "squash" or "fast-forward" when it lands, "reason": "one short line the owner reads on the task"}`)
	return b.String()
}

// parsePMLanding reads the PM's land-or-hold answer. Both land and reason are
// needed: the owner reads the reason whichever it decides. A landing that
// names no way lands as one commit, as every push landing did before.
func parsePMLanding(reply string) (core.LandDecision, error) {
	var in struct {
		Land   *bool  `json:"land"`
		How    string `json:"how"`
		Reason string `json:"reason"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.LandDecision{}, errors.New("the reply was not valid JSON")
	}
	reason := text.Clip(strings.TrimSpace(in.Reason), 300)
	if in.Land == nil || reason == "" {
		return core.LandDecision{}, errors.New(`the reply needs "land" and a "reason"`)
	}
	how := strings.TrimSpace(in.How)
	if !*in.Land {
		how = ""
	} else if how != "" && how != core.LandSquash && how != core.LandKeepCommits {
		return core.LandDecision{}, errors.New(`"how" is "squash" or "fast-forward"`)
	}
	return core.LandDecision{Land: *in.Land, Method: how, Reason: reason}, nil
}
