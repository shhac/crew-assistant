package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
)

// Decision kinds a task can wait on.
const (
	decisionDelivery   = "delivery"
	decisionQuestion   = "question"
	decisionEscalation = "escalation"
	decisionFailure    = "failure"
)

const (
	choiceApprove      = "Approve"
	choiceChanges      = "Request changes"
	choiceAnotherRound = "Another round"
	choiceAcceptDraft  = "Accept this draft"
	choiceStop         = "Stop"
	choiceTryAgain     = "Try again"
	// A role that fails is retried this many times, with growing waits,
	// before the owner hears about it.
	roleRetries = 2
)

// Nudge asks the task loop to look again now, for example after the owner
// answers a decision.
func (a *App) Nudge() { a.nudgeLoop() }

// nudgeLoop asks the loop to look again now rather than at its next tick.
func (a *App) nudgeLoop() {
	select {
	case a.loopWake <- struct{}{}:
	default:
	}
}

// runLoop works tasks one step at a time. Each step is one role turn or one
// state transition, and every step is recorded before the next begins, so a
// restart resumes at the step it was on.
func (a *App) runLoop(ctx context.Context, noDispatch bool) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for {
			progressed, err := a.loopStep(ctx, noDispatch)
			if err != nil && ctx.Err() == nil {
				a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_loop"}, err)
			}
			if !progressed || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-a.loopWake:
		}
	}
}

func (a *App) loopStep(ctx context.Context, noDispatch bool) (bool, error) {
	if a.Demo || noDispatch {
		return false, nil
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	if snap.Paused {
		return false, nil
	}
	if progressed, err := a.settleAnswers(ctx, snap); progressed || err != nil {
		return progressed, err
	}
	t, ok, err := a.Core.NextTask(ctx)
	if err != nil || !ok {
		return false, err
	}
	if t.RetryAt.After(time.Now()) {
		return false, nil
	}
	snap, err = a.Core.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return false, core.ErrNotFound
	}
	m, err := a.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return true, a.roleFailed(ctx, t, "The workspace", err)
	}
	switch t.Status {
	case core.TaskWriting:
		return true, a.write(ctx, p, t, m)
	case core.TaskReviewing:
		return true, a.review(ctx, p, t, m)
	case core.TaskDeciding:
		return true, a.decide(ctx, p, t)
	case core.TaskLanding:
		return true, a.land(ctx, p, t, m)
	}
	return false, nil
}

func findProject(s core.Snapshot, id string) (core.Project, bool) {
	for _, p := range s.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return core.Project{}, false
}

func findDecision(s core.Snapshot, id string) (core.Decision, bool) {
	for _, d := range s.Decisions {
		if d.ID == id {
			return d, true
		}
	}
	return core.Decision{}, false
}

func roleOf(t core.Task, kind string) []core.Role {
	var out []core.Role
	for _, r := range t.Roles {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// taskPlaybook is the setup a task runs under: the one pinned when it
// started, or the project's current one for a task that has not started.
func taskPlaybook(p core.Project, t core.Task) *core.Playbook {
	if t.Playbook != nil {
		return t.Playbook
	}
	return p.Playbook
}

func (a *App) roleSpec(r core.Role, workDir string, write bool, m medium, prompt string) roles.Spec {
	cfg := a.Config()
	spec := roles.Spec{Engine: r.Engine, Model: r.Model, Effort: r.Effort, WorkDir: workDir, Write: write, Env: m.env(), Read: m.readable(), Instructions: r.Instructions, Prompt: prompt}
	if r.Engine == "codex" {
		spec.Binary, spec.Home = cfg.Model.CodexBin, cfg.Model.CodexHome
		spec.RuntimeHome = filepath.Join(a.Core.StateDirectory(), "roles", "codex")
	} else {
		spec.Binary, spec.Home = cfg.Model.ClaudeBin, cfg.Model.ClaudeHome
	}
	return spec
}

// write runs the implementer for this round and records what it produced.
// The workspace is first reset to the last revision, so nothing a crashed or
// failed turn left behind is ever mistaken for a draft.
func (a *App) write(ctx context.Context, p core.Project, t core.Task, m medium) error {
	writers := roleOf(t, core.RoleImplementer)
	if len(writers) != 1 {
		return a.stopTask(ctx, t, "This task's team has no writer")
	}
	if len(t.Revisions) == 0 {
		started, err := m.begin(ctx, t)
		if err != nil {
			return a.roleFailed(ctx, t, "The workspace", err)
		}
		if started.Base != t.Base || started.Branch != t.Branch {
			if t, err = a.Core.UpdateTask(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Base, task.From, task.Branch = started.Base, started.From, started.Branch
				return "", nil
			}); err != nil {
				return err
			}
		}
	}
	if err := m.reset(ctx, t); err != nil {
		return a.roleFailed(ctx, t, "The workspace", err)
	}
	if held, err := a.holdForUsage(ctx, t, writers[0]); held || err != nil {
		return err
	}
	caughtUp := ""
	l, err := m.behind(ctx, t)
	if err != nil {
		return a.roleFailed(ctx, t, "The workspace", err)
	}
	if l != nil {
		moved, clean, conflicts, err := m.catchUp(ctx, t, *l)
		if err != nil {
			return a.roleFailed(ctx, t, "The workspace", fmt.Errorf("catching up: %s: %w", l.What, err))
		}
		if clean != "" && t.CatchUp {
			return a.recordCatchUp(ctx, moved, m, clean, *l)
		}
		if t, err = a.Core.UpdateTask(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
			task.Base, task.From = moved.Base, moved.From
			return "", nil
		}); err != nil {
			return err
		}
		caughtUp = catchUpText(l.What, conflicts)
	}
	woken, err := a.Core.TakeTaskWakes(ctx, t.ID, core.WakeTask)
	if err != nil {
		return err
	}
	prompt, err := a.wakePrompt(ctx, t, woken)
	if err != nil {
		return err
	}
	spec := a.roleSpec(writers[0], m.workspace(), true, m, writerPrompt(p, t, caughtUp)+prompt)
	spec.Resume = t.WriterSession
	result, err := a.runner.Run(ctx, spec)
	if err != nil {
		return a.roleFailed(ctx, t, writers[0].Name, err)
	}
	reply, block := splitWakeBlock(result.Text)
	wakeErrors := a.applyWakeBlock(ctx, p, t, block)
	n := len(t.Revisions) + 1
	revision, err := m.snapshot(ctx, t, n)
	if errors.Is(err, gitrepo.ErrNoChange) && proposed(t) {
		// Feedback on a pull request can need no change; the team says why.
		_, err = a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.Finished() {
				return "", nil
			}
			t.WriterSession, t.WakeErrors, t.Failures, t.RetryAt = result.Session, wakeErrors, 0, time.Time{}
			t.Status, t.Detail = core.TaskLanding, "No change needed: "+clip(reply, 300)
			return fmt.Sprintf("%s: no change needed for the pull request's feedback", t.Objective), nil
		})
		return err
	}
	if err != nil {
		return a.roleFailed(ctx, t, writers[0].Name, fmt.Errorf("the work could not be recorded: %w", err))
	}
	_, err = a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		revision.BriefVersion, revision.Summary, revision.At = p.Brief.Version, clip(reply, 2000), time.Now().UTC()
		t.Revisions = append(t.Revisions, revision)
		t.WriterSession, t.WakeErrors = result.Session, wakeErrors
		t.Failures, t.RetryAt, t.CatchUp = 0, time.Time{}, false
		t.Status, t.Detail = core.TaskReviewing, fmt.Sprintf("Draft %d written; reviewing", n)
		return fmt.Sprintf("%s wrote draft %d of %s", writers[0].Name, n, t.Objective), nil
	})
	return err
}

// review runs each checking role that has not yet judged the latest revision
// against the current brief, one per step: reviewers first, then QA.
func (a *App) review(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if len(t.Revisions) == 0 {
		return a.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	r := t.Revisions[len(t.Revisions)-1]
	for _, checker := range append(roleOf(t, core.RoleReviewer), roleOf(t, core.RoleQA)...) {
		if judged(t, checker.Name, r.N, p.Brief.Version) {
			continue
		}
		if held, err := a.holdForUsage(ctx, t, checker); held || err != nil {
			return err
		}
		verdict, err := a.runChecker(ctx, p, t, r, checker, m)
		if err != nil {
			return a.roleFailed(ctx, t, checker.Name, err)
		}
		_, err = a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
			if t.Status == core.TaskStopped {
				return "", nil
			}
			verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, checker.Name, p.Brief.Version, time.Now().UTC()
			t.Verdicts = append(t.Verdicts, verdict)
			t.Failures, t.RetryAt = 0, time.Time{}
			return fmt.Sprintf("%s checked draft %d: %s", checker.Name, r.N, verdict.Outcome), nil
		})
		return err
	}
	return a.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
}

func judged(t core.Task, role string, revision, briefVersion int) bool {
	for _, v := range t.Verdicts {
		if v.Role == role && v.Revision == revision && v.BriefVersion == briefVersion {
			return true
		}
	}
	return false
}

// runChecker gives a fresh checking session the revision to judge. Only QA
// may write, to run the check; the medium discards whatever it wrote. A reply
// that is not a usable verdict gets one plain retry.
func (a *App) runChecker(ctx context.Context, p core.Project, t core.Task, r core.Revision, checker core.Role, m medium) (core.Verdict, error) {
	dir, cleanup, err := m.checkDir(ctx, t, r)
	if err != nil {
		return core.Verdict{}, err
	}
	defer cleanup()
	playbook := taskPlaybook(p, t)
	base := checkerPrompt(p, t, r, checker, playbook)
	prompt := base
	var parseErr error
	for attempt := 0; attempt < 2; attempt++ {
		result, err := a.runner.Run(ctx, a.roleSpec(checker, dir, checker.Kind == core.RoleQA, m, prompt))
		if err != nil {
			return core.Verdict{}, err
		}
		verdict, err := parseVerdict(result.Text)
		if err == nil {
			return verdict, nil
		}
		parseErr = err
		prompt = base + "\n\nYour previous reply could not be used (" + err.Error() + "). Reply with only the JSON object."
	}
	return core.Verdict{}, parseErr
}

// decide turns the latest reviews into the next step. Deterministic: revise
// until the round limit, bring the owner questions, a limit reached, or a
// draft every reviewer passed.
func (a *App) decide(ctx context.Context, p core.Project, t core.Task) error {
	if len(t.Revisions) == 0 {
		return a.setStatus(ctx, t.ID, core.TaskWriting, "")
	}
	r := t.Revisions[len(t.Revisions)-1]
	var current []core.Verdict
	for _, v := range t.Verdicts {
		if v.Revision == r.N && v.BriefVersion == p.Brief.Version {
			current = append(current, v)
		}
	}
	if len(current) < len(roleOf(t, core.RoleReviewer)) {
		// The brief changed after some reviews: judge again against it.
		return a.setStatus(ctx, t.ID, core.TaskReviewing, "Re-checking against the updated brief")
	}
	var questions, changes []core.Verdict
	for _, v := range current {
		switch v.Outcome {
		case core.VerdictQuestion:
			questions = append(questions, v)
		case core.VerdictRevise:
			changes = append(changes, v)
		}
	}
	switch {
	case r.BriefVersion != p.Brief.Version && len(changes) == 0 && len(questions) == 0:
		// Passed against the new brief even though it was written for the old
		// one; that is still a pass.
		fallthrough
	case len(questions) == 0 && len(changes) == 0:
		return a.askForDelivery(ctx, p, t, r, current)
	case len(questions) > 0:
		q := questions[0]
		_, err := a.Core.OpenTaskDecision(ctx, t.ID, decisionQuestion, core.DecisionInput{
			Title:          fmt.Sprintf("%s has a question about %s", q.Role, t.Objective),
			Context:        q.Question + "\n\nAnswer in your own words; the writer revises with your answer.",
			Recommendation: "Answer the question so the next draft can meet the brief",
			Choices:        []string{"Use your judgement", choiceStop},
		})
		return err
	case t.Round >= t.MaxRounds:
		_, err := a.Core.OpenTaskDecision(ctx, t.ID, decisionEscalation, core.DecisionInput{
			Title:          fmt.Sprintf("%s still has review points after %d rounds", t.Objective, t.Round),
			Context:        reviewDigest(changes),
			Recommendation: "Another round if the points matter; otherwise accept this draft",
			Choices:        []string{choiceAnotherRound, choiceAcceptDraft, choiceStop},
		})
		return err
	default:
		_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.Status == core.TaskStopped {
				return "", nil
			}
			t.Round++
			t.Status, t.Detail = core.TaskWriting, fmt.Sprintf("Revising (round %d of %d)", t.Round, t.MaxRounds)
			return fmt.Sprintf("Round %d of %s: revising after review", t.Round, t.Objective), nil
		})
		return err
	}
}

func reviewDigest(verdicts []core.Verdict) string {
	var b strings.Builder
	for _, v := range verdicts {
		fmt.Fprintf(&b, "%s: %s\n", v.Role, v.Summary)
		for _, f := range v.Findings {
			fmt.Fprintf(&b, "- %s\n", f.Note)
		}
	}
	return strings.TrimSpace(b.String())
}

func (a *App) askForDelivery(ctx context.Context, p core.Project, t core.Task, r core.Revision, verdicts []core.Verdict) error {
	m, err := a.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return a.roleFailed(ctx, t, "The workspace", err)
	}
	l, err := m.behind(ctx, t)
	if err != nil {
		return a.roleFailed(ctx, t, "The workspace", err)
	}
	if l != nil {
		return a.catchUpRound(ctx, t, *l)
	}
	if approvalStands(t) || !taskPlaybook(p, t).Land.AsksFirst() || proposed(t) {
		return a.resumeLanding(ctx, t)
	}
	where := m.deliveryNote(t)
	_, err = a.Core.OpenTaskDecision(ctx, t.ID, decisionDelivery, core.DecisionInput{
		Title:          fmt.Sprintf("Ready to approve: %s (draft %d)", t.Objective, r.N),
		Context:        clip(r.Summary, 600) + "\n\nReviews:\n" + reviewDigest(verdicts) + "\n\n" + where,
		Recommendation: choiceApprove,
		Choices:        []string{choiceApprove, choiceChanges},
	})
	return err
}

// roleFailed retries a failing role a couple of times with growing waits,
// then brings the owner one decision. A sandbox or login problem will not
// clear by itself and goes to the owner at once.
func (a *App) roleFailed(ctx context.Context, t core.Task, role string, cause error) error {
	permanent := roles.Permanent(cause)
	var failures int
	updated, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		t.Failures++
		failures = t.Failures
		if permanent || t.Failures > roleRetries {
			return "", nil
		}
		t.RetryAt = time.Now().Add(time.Duration(t.Failures*t.Failures) * time.Minute)
		t.Detail = fmt.Sprintf("%s hit a problem; trying again shortly", role)
		return "", nil
	})
	if err != nil {
		return err
	}
	a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_role", ProjectID: updated.ProjectID}, cause)
	if !permanent && failures <= roleRetries {
		return nil
	}
	if _, err = a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		t.ResumeStatus = t.Status
		return "", nil
	}); err != nil {
		return err
	}
	_, err = a.Core.OpenTaskDecision(ctx, t.ID, decisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("%s couldn't work on %s", role, t.Objective),
		Context:        clip(cause.Error(), 600),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

func (a *App) setStatus(ctx context.Context, id, status, detail string) error {
	_, err := a.Core.UpdateTask(ctx, id, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		t.Status, t.Detail = status, detail
		return "", nil
	})
	return err
}

func (a *App) stopTask(ctx context.Context, t core.Task, reason string) error {
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		t.Status, t.Detail = core.TaskStopped, reason
		return t.Objective + " stopped: " + reason, nil
	})
	return err
}

// settleAnswers applies the owner's answers to decisions tasks are waiting on.
func (a *App) settleAnswers(ctx context.Context, snap core.Snapshot) (bool, error) {
	for _, t := range snap.Tasks {
		if t.Status != core.TaskWaiting || t.DecisionID == "" {
			continue
		}
		d, ok := findDecision(snap, t.DecisionID)
		if !ok || d.Status == "open" {
			continue
		}
		p, ok := findProject(snap, t.ProjectID)
		if !ok {
			continue
		}
		return true, a.applyAnswer(ctx, p, t, d)
	}
	return false, nil
}

func (a *App) applyAnswer(ctx context.Context, p core.Project, t core.Task, d core.Decision) error {
	if d.Status == "dismissed" {
		return a.stopTask(ctx, t, "the owner said it is no longer needed")
	}
	answer := strings.TrimSpace(d.Answer)
	switch {
	case strings.EqualFold(answer, choiceStop):
		return a.stopTask(ctx, t, "the owner stopped it")
	case d.Kind == decisionDelivery && strings.EqualFold(answer, choiceApprove),
		d.Kind == decisionEscalation && strings.EqualFold(answer, choiceAcceptDraft):
		return a.approve(ctx, t)
	case d.Kind == decisionFailure && strings.EqualFold(answer, choiceTryAgain):
		_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			if t.Status == core.TaskStopped {
				return "", nil
			}
			t.Status, t.ResumeStatus = t.ResumeStatus, ""
			if t.Status == "" {
				t.Status = core.TaskWriting
			}
			// The owner's retry starts the count of catch-ups afresh.
			t.Failures, t.RetryAt, t.DecisionID, t.Detail, t.CatchUps = 0, time.Time{}, "", "Trying again", 0
			return "Trying " + t.Objective + " again", nil
		})
		return err
	}
	// Anything else is direction for another round: the owner asked for
	// changes, answered a reviewer's question or wants one more attempt.
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		switch {
		case d.Kind == decisionQuestion:
			t.Direction = append(t.Direction, "Answer to a reviewer's question ("+clip(d.Context, 300)+"): "+answer)
		case !strings.EqualFold(answer, choiceAnotherRound) && !strings.EqualFold(answer, choiceChanges):
			t.Direction = append(t.Direction, answer)
		}
		t.Round++
		if t.MaxRounds < t.Round {
			t.MaxRounds = t.Round
		}
		t.Status, t.DecisionID, t.Detail = core.TaskWriting, "", fmt.Sprintf("Revising with your direction (round %d)", t.Round)
		return fmt.Sprintf("Revising %s with the owner's direction", t.Objective), nil
	})
	return err
}

// holdForUsage waits a task out while the role's subscription is past the
// owner's threshold. A hold is a wait, not a failure: nothing is retried or
// counted against the task, and it resumes by itself when the window resets
// or the threshold is raised.
func (a *App) holdForUsage(ctx context.Context, t core.Task, r core.Role) (bool, error) {
	cfg := a.Config()
	threshold, supported := quota.Threshold(cfg.Limits.RoleUsage, r.Engine)
	if !supported || threshold == 0 {
		return false, nil
	}
	model := cfg.Model
	model.Engine, model.Model = r.Engine, r.Model
	now := time.Now()
	verdict := quota.Evaluate(a.meter.Read(ctx, model), model, threshold, now)
	wait, detail := time.Time{}, ""
	switch {
	case verdict.Held:
		wait, detail = verdict.ResetsAt, fmt.Sprintf("Waiting for %s subscription headroom (%s)", r.Engine, verdict.Detail)
		if !wait.After(now) {
			wait = now.Add(10 * time.Minute)
		}
	case !verdict.Known && cfg.Limits.RoleUsage.OnUnavailable == "pause":
		wait, detail = now.Add(5*time.Minute), "Waiting until "+r.Engine+" usage can be checked"
	default:
		return false, nil
	}
	_, err := a.Core.UpdateTask(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.Status == core.TaskStopped {
			return "", nil
		}
		t.RetryAt, t.Detail = wait, detail
		return "", nil
	})
	return true, err
}

// StopTask ends a task at the owner's request. A turn already running
// finishes, but nothing it reports can restart the task, and any decision the
// task was waiting on is closed as no longer needed.
func (a *App) StopTask(ctx context.Context, projectID, taskID string) (core.Task, error) {
	var decisionID string
	stopped, err := a.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.ProjectID != projectID {
			return "", core.ErrNotFound
		}
		if t.Finished() {
			return "", fmt.Errorf("this task has already finished: %w", core.ErrConflict)
		}
		decisionID = t.DecisionID
		t.Status, t.DecisionID, t.Detail = core.TaskStopped, "", "the owner stopped it"
		return t.Objective + " stopped by the owner", nil
	})
	if err != nil {
		return stopped, err
	}
	if decisionID != "" {
		if _, dismissErr := a.Core.DismissDecision(ctx, decisionID, "The task was stopped"); dismissErr != nil && !errors.Is(dismissErr, core.ErrConflict) {
			return stopped, dismissErr
		}
	}
	a.nudgeLoop()
	return stopped, nil
}
