package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
)

// review moves a task whose latest revision every checker group has judged
// against the current brief on to deciding. The checks themselves each run
// as a step of their own, side by side; see check.
func (lp *Loop) review(ctx context.Context, p core.Project, t core.Task) error {
	r := t.Revisions[len(t.Revisions)-1]
	for _, checker := range t.Checkers() {
		if !t.Judged(checker.Name, r.N, p.Brief.Version) {
			return nil
		}
	}
	return lp.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
}

// check runs one checker seat on the latest revision, for its group: a
// reviewer and QA check the same draft at once, each in a copy of its own.
// A group that judged it meanwhile, such as by answering a message, is not
// asked again.
func (lp *Loop) check(ctx context.Context, p core.Project, t core.Task, m medium, checker core.Role) error {
	r := t.Revisions[len(t.Revisions)-1]
	if t.Judged(checker.Name, r.N, p.Brief.Version) {
		return nil
	}
	if held, err := lp.holdForUsage(ctx, t, checker); held || err != nil {
		return err
	}
	verdict, shots, end, err := lp.runCheckerAwake(ctx, p, t, r, checker, m)
	if err != nil {
		return lp.roleFailed(ctx, t, checker.Name, err)
	}
	if lp.checked != nil {
		lp.checked(t.ID, checker.Name)
	}
	// The verdict judged the text the checker was shown: if it or anyone
	// changed the objective or criteria meanwhile, it judges again.
	verdict.TextVersion = t.TextVersion
	// The screenshots are kept only with the verdict, in the same update:
	// as updateOpen, a finished task records neither.
	_, err = lp.Core.UpdateTaskWithVerdict(ctx, t.ID, verdict, shots, func(t *core.Task, p *core.Project, verdict core.Verdict) (string, error) {
		if t.Finished() {
			return "", nil
		}
		// Ref is what the checker's own copy held, set with the verdict.
		verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, checker.Name, p.Brief.Version, time.Now().UTC()
		t.Verdicts = append(t.Verdicts, verdict)
		t.Failures, t.RetryAt = 0, time.Time{}
		return fmt.Sprintf("%s checked version %d of %s: %s", checker.Name, r.N, t.Objective, outcomeWords[verdict.Outcome]), nil
	}, end)
	return err
}

// sleepAllowance is how long a check may sleep through before a failure it
// found is put down to the sleep; a moment's sleep fires no timeouts.
const sleepAllowance = time.Minute

// runCheckerAwake runs a check once more when the machine slept through it
// and it failed: a test's timeout keeps counting while a laptop's lid is
// shut, so the failure may be the sleep's. A pass stands, since a sleep
// fails checks and never passes them, as does a second run that slept too.
func (lp *Loop) runCheckerAwake(ctx context.Context, p core.Project, t core.Task, r core.Revision, checker core.Role, m medium) (core.Verdict, core.Screenshots, core.TurnEnd, error) {
	start := time.Now()
	verdict, shots, end, err := lp.runChecker(ctx, p, t, r, checker, m, "")
	if err != nil || verdict.Outcome == core.VerdictPass || lp.sleptSince(start) < sleepAllowance {
		return verdict, shots, end, err
	}
	return lp.runChecker(ctx, p, t, r, checker, m, "")
}

// sleptSince is how long the machine slept since start: the wall clock
// counts a sleep and the monotonic clock doesn't, on macOS and Linux alike.
func (lp *Loop) sleptSince(start time.Time) time.Duration {
	if lp.slept != nil {
		return lp.slept(start)
	}
	return time.Now().Round(0).Sub(start.Round(0)) - time.Since(start)
}

// runChecker gives a fresh checking session the revision to judge, in a copy
// of its own that no one else works in. Only QA may write, and only to its
// scratch folder, to run the check. The copy must still be exactly the
// revision afterwards, or the verdict is discarded and the check fails, to
// run again. A reply that is not a usable verdict gets one plain retry.
// Where the project has a run recipe, the daemon starts the app for QA, on
// a port of its own, and gives the screenshots it took, to keep with its
// verdict when that is recorded.
func (lp *Loop) runChecker(ctx context.Context, p core.Project, t core.Task, r core.Revision, checker core.Role, m medium, note string) (core.Verdict, core.Screenshots, core.TurnEnd, error) {
	playbook := taskPlaybook(p, t)
	app, err := lp.planApp(checker, playbook)
	if err != nil {
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, err
	}
	c, err := m.check(ctx, t, r, checker.Holds(core.RoleQA), app.running())
	if err != nil {
		app.release()
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, err
	}
	app.tree = c.tree
	app.cleanupError = lp.commandCleanup
	// The port is held until the copy the app ran from is gone.
	defer func() {
		app.stop()
		c.remove()
		app.release()
	}()
	app.start(ctx, lp, append(m.readable(), c.read...))
	checkoutNote := c.note
	if c.hostedNote != "" {
		checkoutNote = c.hostedNote
	}
	base := checkerPrompt(p, t, r, checker, playbook) + checkoutNote + appPrompt(app) + note + learnedGuide(checker, true) + handOnGuide(t, checker.Working(), t.CheckerGroup(checker.Name))
	kind := core.RoleReviewer
	if checker.Holds(core.RoleQA) {
		kind = core.RoleQA
	}
	spec, cleanupLearnings, err := lp.roleSpec(t, checker, c.workDir, c.write, m, base, &c, kind)
	if err != nil {
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, err
	}
	defer cleanupLearnings()
	if c.env != nil {
		spec.Env = c.env
	}
	spec.Read = append(append([]string(nil), spec.Read...), c.read...)
	app.cleanupError = lp.commandCleanupFor(spec.Observer)
	app.apply(&spec)
	var shots *screenshots
	if app.running() && app.images {
		shots = &screenshots{next: spec.Observer}
		spec.Observer = shots
	}
	var verdict core.Verdict
	var end core.TurnEnd
	_, researches := t.Researcher()
	_, learned, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		clean, block := splitHandOn(reply)
		verdict, err = parseVerdict(clean, researches)
		end.HandOnWhy, end.Problems = parseHandOn(block)
		return err
	})
	if err != nil {
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, err
	}
	if parseErr != nil {
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, parseErr
	}
	if err = c.verify(ctx); err != nil {
		return core.Verdict{}, core.Screenshots{}, core.TurnEnd{}, fmt.Errorf("its verdict was discarded: %w", err)
	}
	lp.recordLearned(ctx, p, t, checker, m, learned)
	verdict.Ref = c.ref
	if app.unavailable != "" {
		verdict.Findings = append(verdict.Findings, core.Finding{Criterion: "Running the app", Note: app.unavailable})
	}
	var taken core.Screenshots
	if shots != nil {
		taken = shots.files(checker.Name)
	}
	return verdict, taken, end, nil
}

// askForJSON runs a role and reads its reply with parse, asking once more,
// with the reason, when the reply can't be read. It gives the reply it
// settled on, the one parse accepted or else the last, which a caller may
// still use as written, and that reply's learned block, so what a role
// learned is recorded once. parseErr is why the last reply couldn't be read;
// runErr is the role failing to run at all.
// retry, when supplied, refreshes a role's state before its correction. Roles
// that already attached unfinished work can discard it and rebuild their tools.
func (lp *Loop) askForJSON(ctx context.Context, spec roles.Spec, parse func(reply string) error, retry ...func(roles.Spec, error) (roles.Spec, error)) (reply, learned string, parseErr, runErr error) {
	base := spec.Prompt
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runRole(ctx, spec)
		if err != nil {
			return reply, learned, parseErr, err
		}
		reply, learned = splitBlock(result.Text, "learned")
		if learned == "" {
			learned = learnedField(reply)
		}
		if parseErr = parse(reply); parseErr == nil {
			return reply, learned, nil, nil
		}
		spec.Prompt = base + "\n\nYour previous reply could not be used (" + parseErr.Error() + "). Reply with only the JSON object."
		if attempt == 0 && len(retry) > 0 && retry[0] != nil {
			spec, err = retry[0](spec, parseErr)
			if err != nil {
				return reply, learned, parseErr, err
			}
		}
		spec.PreviousID, spec.RetryCause = result.AttemptID, "malformed_reply"
	}
	return reply, learned, parseErr, nil
}

// decide turns the latest reviews into the next step: bring the owner
// questions, send the task back to the researcher when a checker asks for
// research, revise until the round limit, bring the owner a limit reached,
// or a draft every reviewer passed. It is deterministic, except where a
// checker recommends another step and the PM chooses; see route.
func (lp *Loop) decide(ctx context.Context, p core.Project, t core.Task) error {
	r := t.Revisions[len(t.Revisions)-1]
	var current []core.Verdict
	for _, v := range t.Verdicts {
		if v.Revision == r.N && t.Counts(v, p.Brief.Version) {
			current = append(current, v)
		}
	}
	for _, checker := range t.Checkers() {
		if !t.Judged(checker.Name, r.N, p.Brief.Version) {
			// The brief or the task's requirements changed after some checks:
			// judge again against them.
			return lp.setStatus(ctx, t.ID, core.TaskReviewing, "Checking again against the updated brief or requirements")
		}
	}
	var questions []core.Verdict
	next := core.NextLand
	for _, v := range current {
		switch v.Outcome {
		case core.VerdictQuestion:
			questions = append(questions, v)
		case core.VerdictResearch:
			next = core.NextResearch
		case core.VerdictRevise:
			if next == core.NextLand {
				next = core.NextRevise
			}
		}
	}
	// A question goes to the owner, and the answer back to the checker that
	// asked it.
	if len(questions) > 0 {
		q := questions[0]
		_, err := lp.Core.AskQuestion(ctx, t.ID, core.Asker{From: q.Role, Step: core.TaskReviewing, Revision: r.N}, core.DecisionInput{
			Title:          fmt.Sprintf("%s has a question about “%s”", q.Role, t.Objective),
			Context:        q.Question,
			Recommendation: "Answer it, or let the team decide",
			Choices:        []string{"Use your judgment", choiceStop},
		})
		return err
	}
	next, busy := lp.route(ctx, p, t, current, next)
	if busy != "" {
		return lp.waitForSeat(ctx, t, busy)
	}
	// The PM's tools can change the task while it chooses. Checks against
	// requirements that have since changed no longer count, so the task is
	// checked again before it goes anywhere; one that moved on is left.
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	now, ok := findTask(snap, "", t.ID)
	if !ok || now.Status != core.TaskDeciding {
		return nil
	}
	if now.TextVersion != t.TextVersion {
		return lp.setStatus(ctx, t.ID, core.TaskReviewing, "Checking again against the updated requirements")
	}
	changes := slices.DeleteFunc(slices.Clone(current), func(v core.Verdict) bool {
		return v.Outcome != core.VerdictRevise && v.Next != core.NextRevise
	})
	switch {
	// A requirement the implementer can't meet from its sandbox is judged
	// first, wherever the checks send the draft: never landed without the
	// owner taking it on, nor sent back for a round it can't win.
	case len(now.Pending(r.N)) > 0:
		return lp.proposeOwnerStep(ctx, p, now, now.Pending(r.N)[0])
	case now.NeedsAssetIntegration():
		return lp.setStatus(ctx, now.ID, core.TaskWriting, "Integrate delivered assets and provenance in a new draft")
	// A draft written for an older brief that passes against the current one
	// is still a pass.
	case next == core.NextLand:
		return lp.askForDelivery(ctx, p, t, r)
	case next == core.NextResearch:
		return lp.askResearch(ctx, t, r, current)
	case t.Round >= t.MaxRounds:
		return lp.escalate(ctx, p, t, current, changes)
	default:
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			// Checks failing on a change the PM approved, merged with what
			// landed since, fail that landing.
			landingFailure(t, "The checks failed on it merged with what landed since: "+reviewDigest(changes))
			t.NextRound()
			t.Status, t.Detail = core.TaskWriting, ""
			return fmt.Sprintf("Round %d of %s: revising after review", t.Round, t.Objective), nil
		})
		return err
	}
}

// askResearch sends the task back to the researcher for the checker that
// asked for research, or recommended it, with what it wants found out.
func (lp *Loop) askResearch(ctx context.Context, t core.Task, r core.Revision, current []core.Verdict) error {
	i := slices.IndexFunc(current, func(v core.Verdict) bool { return v.Outcome == core.VerdictResearch })
	if i < 0 {
		i = slices.IndexFunc(current, func(v core.Verdict) bool { return v.Next == core.NextResearch })
	}
	if i < 0 {
		return fmt.Errorf("no check asked for research on %s", t.Objective)
	}
	v := current[i]
	question := v.Question
	if v.Outcome != core.VerdictResearch {
		question = strings.TrimSpace(v.Note + " " + v.Summary)
	}
	_, err := lp.Core.AskResearch(ctx, t.ID, core.ResearchAsk{
		From:     v.Role,
		Revision: r.N,
		Question: question,
		Owner: core.DecisionInput{
			Title:          fmt.Sprintf("%s wants more research on “%s”", v.Role, t.Objective),
			Context:        question,
			Recommendation: "Answer it, or let the team use its judgment",
			Choices:        []string{"Use your judgment", choiceStop},
		},
	})
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// route is where the checks send the task next. Where a checker recommends
// a step other than the one the checks lead to, the project's PM, if it has
// one, chooses between them; otherwise, or when the PM can't say, the checks
// decide as they always do. A PM at work on something else is waited for:
// it reports the PM's seat as busy, and the task is decided again once the
// seat is free.
func (lp *Loop) route(ctx context.Context, p core.Project, t core.Task, current []core.Verdict, next string) (string, string) {
	options := []string{next}
	_, researches := t.Researcher()
	for _, v := range current {
		if v.Disagrees() && !slices.Contains(options, v.Next) && (v.Next != core.NextResearch || researches) {
			options = append(options, v.Next)
		}
	}
	seat, ok := p.PMSeat()
	if len(options) == 1 || !ok {
		return next, ""
	}
	if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
		return next, ""
	}
	if held, err := lp.holdSeat(ctx, t.ID, seat.Name); err != nil || !held {
		return next, seat.Name
	}
	dir, err := lp.pmWorkDir()
	if err != nil {
		return next, ""
	}
	spec := lp.baseSpec(seat, dir, routePrompt(p, t, current, options))
	spec.ProjectID, spec.Role = p.ID, core.RolePM
	spec.TaskID = t.ID
	spec.Observer = lp.watchTurn(t, core.RolePM, seat, dir, false)
	lp.withTools(&spec, lp.managerTools(p.ID, seat))
	var choice routeChoice
	_, _, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		choice, err = parseRoute(reply, options)
		return err
	})
	if err != nil || parseErr != nil {
		return next, ""
	}
	_ = lp.Core.RecordRoute(ctx, t.ID, seat.Name, nextWords[choice.Next], choice.Reason)
	return choice.Next, ""
}

type routeChoice struct {
	Next   string `json:"next"`
	Reason string `json:"reason"`
}

// parseRoute reads the PM's choice of where a task goes, which must be one
// it was offered.
func parseRoute(reply string, options []string) (routeChoice, error) {
	var in routeChoice
	if err := decodeReply(reply, &in); err != nil {
		return routeChoice{}, errors.New("the reply was not valid JSON")
	}
	in.Next, in.Reason = strings.TrimSpace(in.Next), text.Clip(strings.TrimSpace(in.Reason), 300)
	if !slices.Contains(options, in.Next) {
		return routeChoice{}, fmt.Errorf("next must be one of %s", strings.Join(options, ", "))
	}
	return in, nil
}

// routePrompt asks the PM where a task goes after its checks, when a
// checker recommends something other than what the checks lead to.
func routePrompt(p core.Project, t core.Task, current []core.Verdict, options []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the work moving for the project %s. Goal: %s\n\nTask %s: %s\n", p.Title, p.Brief.Goal, t.Label(), t.Objective)
	if len(t.Criteria) > 0 {
		b.WriteString("Its criteria:\n" + numbered(t.Criteria))
	}
	fmt.Fprintf(&b, "\nThe checks of draft %d:\n", len(t.Revisions))
	for _, v := range current {
		fmt.Fprintf(&b, "- %s: %s. %s\n", v.Role, v.Outcome, v.Summary)
		if v.Next != "" || v.Note != "" {
			fmt.Fprintf(&b, "  recommends %s: %s\n", orDash(v.Next), orDash(v.Note))
		}
	}
	b.WriteString("\nDecide where this task goes next. The checks on their own send it to " + nextWords[options[0]] + ", but a checker recommends otherwise. The steps you can choose:\n")
	for _, o := range options {
		fmt.Fprintf(&b, "- %s: %s\n", o, nextWords[o])
	}
	b.WriteString(`
Weigh what each checker said and why. Do not build or approve anything yourself; the owner's approval rules still apply after you choose. Your tools can tidy tasks, link them, queue a split or a sibling and leave notes, if what you read calls for it.

Reply with only this JSON object:
{"next": "one of the steps above", "reason": "one line on why"}`)
	return b.String() + assetRule(t)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var nextWords = map[string]string{
	core.NextLand:     "approval or landing",
	core.NextRevise:   "the implementer, to revise",
	core.NextResearch: "the researcher, for more research",
}

func reviewDigest(verdicts []core.Verdict) string {
	var b strings.Builder
	for _, v := range verdicts {
		fmt.Fprintf(&b, "%s: %s\n", v.Role, v.Summary)
		for _, f := range v.Findings {
			fmt.Fprintf(&b, "- %s\n", f.Note)
		}
		if v.Note != "" {
			fmt.Fprintf(&b, "- %s\n", v.Note)
		}
	}
	return strings.TrimSpace(b.String())
}

var outcomeWords = map[string]string{core.VerdictPass: "passed", core.VerdictRevise: "asked for changes", core.VerdictQuestion: "asked a question", core.VerdictResearch: "asked for more research"}
