// Package work runs a project's team: the loop that takes each task through
// writing, checking, the owner's approval and landing, and the watcher that
// wakes agents when what they wait on changes. The assistant and the
// dashboard drive it through Loop's methods.
package work

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
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

// Loop runs the teams' tasks. One task step runs at a time, across every
// project.
type Loop struct {
	Core   *core.Service
	Config func() config.Config
	// Diagnostics is set before the loop starts.
	Diagnostics *diagnostics.Logger
	Demo        bool
	runner      roles.Runner
	meter       *quota.Meter
	// github reads and merges pull requests; githubURL is where git pushes.
	// Both are replaced in tests.
	github    github.Client
	githubURL func(repo string) string
	prSeen    sync.Map
	loopWake  chan struct{}
}

func New(s *core.Service, cfg func() config.Config, demo bool) *Loop {
	return &Loop{Core: s, Config: cfg, Demo: demo, runner: roles.Native{}, meter: &quota.Meter{}, github: github.New(), githubURL: github.URL, loopWake: make(chan struct{}, 1)}
}

// Nudge asks the loop to look again now rather than at its next tick, for
// example after the owner answers a decision.
func (lp *Loop) Nudge() {
	select {
	case lp.loopWake <- struct{}{}:
	default:
	}
}

// Run works tasks one step at a time. Each step is one role turn or one
// state transition, and every step is recorded before the next begins, so a
// restart resumes at the step it was on.
func (lp *Loop) Run(ctx context.Context, noDispatch bool) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for {
			progressed, err := lp.loopStep(ctx, noDispatch)
			if err != nil && ctx.Err() == nil {
				lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_loop"}, err)
			}
			if !progressed || err != nil || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-lp.loopWake:
		}
	}
}

func (lp *Loop) loopStep(ctx context.Context, noDispatch bool) (bool, error) {
	if lp.Demo || noDispatch {
		return false, nil
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	if snap.Paused {
		return false, nil
	}
	if progressed, err := lp.settleAnswers(ctx, snap); progressed || err != nil {
		return progressed, err
	}
	if progressed, err := lp.answerMessage(ctx, snap); progressed || err != nil {
		return progressed, err
	}
	t, ok, err := lp.Core.NextTask(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		lp.sweepLearnings(snap)
		return false, nil
	}
	if t.RetryAt.After(time.Now()) {
		return false, nil
	}
	snap, err = lp.Core.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	p, ok := findProject(snap, t.ProjectID)
	if !ok {
		return false, core.ErrNotFound
	}
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return true, lp.roleFailed(ctx, t, "The workspace", err)
	}
	if sent, err := lp.backToWriter(ctx, t); sent {
		return true, err
	}
	switch t.Status {
	case core.TaskWriting:
		return true, lp.write(ctx, p, t, m)
	case core.TaskReviewing:
		return true, lp.review(ctx, p, t, m)
	case core.TaskDeciding:
		return true, lp.decide(ctx, p, t)
	case core.TaskLanding:
		return true, lp.land(ctx, p, t, m)
	}
	return false, nil
}

// backToWriter sends a task past writing back to the implementer when it has
// no revision to work from, or has direction it has not yet had in view. It
// reports whether it did.
func (lp *Loop) backToWriter(ctx context.Context, t core.Task) (bool, error) {
	switch {
	case t.Status != core.TaskReviewing && t.Status != core.TaskDeciding && t.Status != core.TaskLanding:
		return false, nil
	case len(t.Revisions) == 0:
		return true, lp.setStatus(ctx, t.ID, core.TaskWriting, "")
	case t.DirectionPending > 0:
		return true, lp.takeDirection(ctx, t)
	}
	return false, nil
}

// updateOpen changes a task the loop is still working on. A finished task is
// never changed by the loop; only the owner's own actions reach it.
func (lp *Loop) updateOpen(ctx context.Context, id string, fn func(*core.Task, *core.Project) (string, error)) (core.Task, error) {
	return lp.Core.UpdateTask(ctx, id, func(t *core.Task, p *core.Project) (string, error) {
		if t.Finished() {
			return "", nil
		}
		return fn(t, p)
	})
}

// findTask finds a task in a project; an empty projectID matches any project.
func findTask(s core.Snapshot, projectID, taskID string) (core.Task, bool) {
	for _, t := range s.Tasks {
		if t.ID == taskID && (projectID == "" || t.ProjectID == projectID) {
			return t, true
		}
	}
	return core.Task{}, false
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

// taskPlaybook is the setup a task runs under: the one pinned when it
// started, or the project's current one for a task that has not started.
func taskPlaybook(p core.Project, t core.Task) *core.Playbook {
	if t.Playbook != nil {
		return t.Playbook
	}
	return p.Playbook
}

func (lp *Loop) roleSpec(t core.Task, r core.Role, workDir string, write bool, m medium, prompt string) (roles.Spec, error) {
	cfg := lp.Config()
	spec := roles.Spec{Engine: r.Engine, Model: r.Model, Effort: r.Effort, WorkDir: workDir, Write: write, Env: m.env(), Read: m.readable(), Instructions: r.Instructions, Prompt: prompt}
	dir, index, err := lp.learningsIndex(t, r)
	if err != nil {
		return spec, err
	}
	if index != "" {
		spec.Read = append(append([]string(nil), spec.Read...), dir)
		spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + index)
	}
	if r.Engine == "codex" {
		spec.Binary, spec.Home = cfg.Model.CodexBin, cfg.Model.CodexHome
		spec.RuntimeHome = filepath.Join(lp.Core.StateDirectory(), "roles", "codex")
	} else {
		spec.Binary, spec.Home = cfg.Model.ClaudeBin, cfg.Model.ClaudeHome
	}
	return spec, nil
}

// write runs the implementer for this round and records what it produced.
// The workspace is first reset to the last revision, so nothing a crashed or
// failed turn left behind is ever mistaken for a draft.
func (lp *Loop) write(ctx context.Context, p core.Project, t core.Task, m medium) error {
	writers := t.RolesOf(core.RoleImplementer)
	if len(writers) != 1 {
		return lp.stopTask(ctx, t, "This task's team has no writer")
	}
	t, err := lp.prepareWorkspace(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	if held, err := lp.holdForUsage(ctx, t, writers[0]); held || err != nil {
		return err
	}
	t, caughtUp, err := lp.takeInLanded(ctx, t, m)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	woken, err := lp.Core.TakeTaskWakes(ctx, t.ID, core.WakeTask)
	if err != nil {
		return err
	}
	prompt, err := lp.wakePrompt(ctx, t, woken)
	if err != nil {
		return err
	}
	seen := len(t.Direction)
	spec, err := lp.roleSpec(t, writers[0], m.workspace(), true, m, writerPrompt(p, t, caughtUp)+prompt)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	spec.Resume = t.WriterSession
	result, err := lp.runner.Run(ctx, spec)
	if err != nil {
		return lp.roleFailed(ctx, t, writers[0].Name, err)
	}
	return lp.recordDraft(ctx, p, t, m, writers[0].Name, result, seen)
}

// prepareWorkspace starts the task's workspace on its first round, then puts
// it back at the last revision.
func (lp *Loop) prepareWorkspace(ctx context.Context, t core.Task, m medium) (core.Task, error) {
	if len(t.Revisions) == 0 {
		started, err := m.begin(ctx, t)
		if err != nil {
			return t, err
		}
		if started.Base != t.Base || started.Branch != t.Branch {
			if t, err = lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
				task.Base, task.From, task.Branch = started.Base, started.From, started.Branch
				return "", nil
			}); err != nil {
				return t, err
			}
		}
	}
	return t, m.reset(ctx, t)
}

// takeInLanded merges what landed since into the workspace, so the
// implementer works on top of it: cleanly if it can be, otherwise with the
// conflicts left for it to resolve. It says what happened, for the prompt.
func (lp *Loop) takeInLanded(ctx context.Context, t core.Task, m medium) (core.Task, string, error) {
	c, l, err := lag(ctx, m, t)
	if err != nil || l == nil {
		return t, "", err
	}
	moved, commit, err := c.cleanMerge(ctx, t, *l)
	var conflicts []string
	if err == nil && commit == "" {
		moved, conflicts, err = c.conflictMerge(ctx, t, *l)
	}
	if err != nil {
		return t, "", fmt.Errorf("catching up: %s: %w", l.What, err)
	}
	t, err = lp.updateOpen(ctx, t.ID, func(task *core.Task, _ *core.Project) (string, error) {
		task.Base, task.From = moved.Base, moved.From
		return "", nil
	})
	return t, catchUpText(l.What, conflicts), err
}

// recordDraft records what the implementer's round produced: a new draft for
// review, or, answering a pull request, the team's word that nothing needed
// to change. seen is how much of the owner's direction its prompt carried.
func (lp *Loop) recordDraft(ctx context.Context, p core.Project, t core.Task, m medium, writer string, result roles.Result, seen int) error {
	reply, block := splitWakeBlock(result.Text)
	wakeErrors := lp.applyWakeBlock(ctx, p, t, block)
	n := len(t.Revisions) + 1
	revision, err := m.snapshot(ctx, t, n)
	if errors.Is(err, gitrepo.ErrNoChange) && proposed(t) {
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.WriterSession, t.WakeErrors, t.Failures, t.RetryAt = result.Session, wakeErrors, 0, time.Time{}
			t.AnswerDirection(seen, 0, "No change needed: "+reply, time.Now().UTC())
			if t.DirectionPending > 0 {
				t.ReviseWithDirection()
				return fmt.Sprintf("%s: no change needed for the pull request; revising with your note", t.Objective), nil
			}
			t.Status, t.Detail = core.TaskLanding, "No change needed: "+text.Clip(reply, 300)
			return fmt.Sprintf("%s: no change needed for the pull request's feedback", t.Objective), nil
		})
		return err
	}
	if err != nil {
		return lp.roleFailed(ctx, t, writer, fmt.Errorf("the work could not be recorded: %w", err))
	}
	_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
		revision.BriefVersion, revision.Summary, revision.At = p.Brief.Version, text.Clip(reply, 2000), time.Now().UTC()
		t.Revisions = append(t.Revisions, revision)
		t.AnswerDirection(seen, n, reply, revision.At)
		t.WriterSession, t.WakeErrors = result.Session, wakeErrors
		t.Failures, t.RetryAt = 0, time.Time{}
		t.Status, t.Detail = core.TaskReviewing, ""
		return fmt.Sprintf("%s finished version %d of %s", writer, n, t.Objective), nil
	})
	return err
}

// review runs each checking role that has not yet judged the latest revision
// against the current brief, one per step: reviewers first, then QA.
func (lp *Loop) review(ctx context.Context, p core.Project, t core.Task, m medium) error {
	r := t.Revisions[len(t.Revisions)-1]
	for _, checker := range t.Checkers() {
		if t.Judged(checker.Name, r.N, p.Brief.Version) {
			continue
		}
		if held, err := lp.holdForUsage(ctx, t, checker); held || err != nil {
			return err
		}
		verdict, err := lp.runChecker(ctx, p, t, r, checker, m, "")
		if err != nil {
			return lp.roleFailed(ctx, t, checker.Name, err)
		}
		_, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, p *core.Project) (string, error) {
			verdict.Revision, verdict.Role, verdict.BriefVersion, verdict.At = r.N, checker.Name, p.Brief.Version, time.Now().UTC()
			t.Verdicts = append(t.Verdicts, verdict)
			t.Failures, t.RetryAt = 0, time.Time{}
			return fmt.Sprintf("%s checked version %d of %s: %s", checker.Name, r.N, t.Objective, outcomeWords[verdict.Outcome]), nil
		})
		return err
	}
	return lp.setStatus(ctx, t.ID, core.TaskDeciding, "Checks are in")
}

// takeDirection sends a task back to the implementer when the owner has told
// it something it has not yet had in view, so the task never reaches approval
// or landing without it.
func (lp *Loop) takeDirection(ctx context.Context, t core.Task) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.DirectionPending == 0 {
			return "", nil
		}
		t.ReviseWithDirection()
		return fmt.Sprintf("Revising %s with your note", t.Objective), nil
	})
	return err
}

// runChecker gives a fresh checking session the revision to judge. Only QA
// may write, to run the check; the medium discards whatever it wrote. A reply
// that is not a usable verdict gets one plain retry.
func (lp *Loop) runChecker(ctx context.Context, p core.Project, t core.Task, r core.Revision, checker core.Role, m medium, note string) (core.Verdict, error) {
	dir, cleanup, err := m.checkDir(ctx, t, r)
	if err != nil {
		return core.Verdict{}, err
	}
	defer cleanup()
	playbook := taskPlaybook(p, t)
	base := checkerPrompt(p, t, r, checker, playbook) + note
	spec, err := lp.roleSpec(t, checker, dir, checker.Kind == core.RoleQA, m, base)
	if err != nil {
		return core.Verdict{}, err
	}
	var parseErr error
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return core.Verdict{}, err
		}
		verdict, err := parseVerdict(result.Text)
		if err == nil {
			return verdict, nil
		}
		parseErr = err
		spec.Prompt = base + "\n\nYour previous reply could not be used (" + err.Error() + "). Reply with only the JSON object."
	}
	return core.Verdict{}, parseErr
}

// decide turns the latest reviews into the next step. Deterministic: revise
// until the round limit, bring the owner questions, a limit reached, or a
// draft every reviewer passed.
func (lp *Loop) decide(ctx context.Context, p core.Project, t core.Task) error {
	r := t.Revisions[len(t.Revisions)-1]
	var current []core.Verdict
	for _, v := range t.Verdicts {
		if v.Revision == r.N && v.BriefVersion == p.Brief.Version {
			current = append(current, v)
		}
	}
	for _, checker := range t.Checkers() {
		if !t.Judged(checker.Name, r.N, p.Brief.Version) {
			// The brief changed after some checks: judge again against it.
			return lp.setStatus(ctx, t.ID, core.TaskReviewing, "Checking again against the updated brief")
		}
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
	// A draft written for an older brief that passes against the current one
	// is still a pass.
	case len(questions) == 0 && len(changes) == 0:
		return lp.askForDelivery(ctx, p, t, r)
	case len(questions) > 0:
		q := questions[0]
		_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionQuestion, core.DecisionInput{
			Title:          fmt.Sprintf("%s has a question about “%s”", q.Role, t.Objective),
			Context:        q.Question,
			Recommendation: "Answer it, or let the team decide",
			Choices:        []string{"Use your judgment", choiceStop},
		})
		return err
	case t.Round >= t.MaxRounds:
		_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionEscalation, core.DecisionInput{
			Title:          fmt.Sprintf("“%s” still has review points after %d rounds", t.Objective, t.Round),
			Context:        reviewDigest(changes),
			Recommendation: "Another round if these points matter; otherwise accept it as it is",
			Choices:        []string{choiceAnotherRound, choiceAcceptDraft, choiceStop},
		})
		return err
	default:
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.NextRound()
			t.Status, t.Detail = core.TaskWriting, ""
			return fmt.Sprintf("Round %d of %s: revising after review", t.Round, t.Objective), nil
		})
		return err
	}
}

// approvalTitle says what approving does.
func approvalTitle(t core.Task, playbook *core.Playbook) string {
	if playbook == nil || playbook.Medium != core.MediumGit {
		return fmt.Sprintf("Approve “%s”", t.Objective)
	}
	switch playbook.Land.Way() {
	case core.LandPush:
		return fmt.Sprintf("Land “%s” on %s", t.Objective, playbook.Land.Target)
	case core.LandPullRequest:
		return fmt.Sprintf("Open a pull request for “%s”", t.Objective)
	}
	return fmt.Sprintf("Create a branch for “%s”", t.Objective)
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

func (lp *Loop) askForDelivery(ctx context.Context, p core.Project, t core.Task, r core.Revision) error {
	m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	c, l, err := lag(ctx, m, t)
	if err != nil {
		return lp.roleFailed(ctx, t, "The workspace", err)
	}
	if l != nil {
		return lp.catchUpRound(ctx, t, c, *l)
	}
	if approvalStands(t) || !taskPlaybook(p, t).Land.AsksFirst() || proposed(t) {
		return lp.resumeLanding(ctx, t)
	}
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionDelivery, core.DecisionInput{
		Title:          approvalTitle(t, taskPlaybook(p, t)),
		Context:        text.Clip(r.Summary, 600) + "\n\n" + m.deliveryNote(t),
		Recommendation: choiceApprove,
		Choices:        []string{choiceApprove, choiceChanges},
	})
	return err
}

// roleFailed retries a failing role a couple of times with growing waits,
// then brings the owner one decision. A sandbox or login problem will not
// clear by itself and goes to the owner at once.
func (lp *Loop) roleFailed(ctx context.Context, t core.Task, role string, cause error) error {
	permanent := roles.Permanent(cause)
	var failures int
	updated, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
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
	lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_role", ProjectID: updated.ProjectID}, cause)
	if !permanent && failures <= roleRetries {
		return nil
	}
	if _, err = lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.ResumeStatus = t.Status
		return "", nil
	}); err != nil {
		return err
	}
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionFailure, core.DecisionInput{
		Title:          fmt.Sprintf("%s couldn't work on “%s”", role, t.Objective),
		Context:        text.Clip(cause.Error(), 600),
		Recommendation: choiceTryAgain + " once the cause is fixed",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

func (lp *Loop) setStatus(ctx context.Context, id, status, detail string) error {
	_, err := lp.updateOpen(ctx, id, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Detail = status, detail
		return "", nil
	})
	return err
}

func (lp *Loop) stopTask(ctx context.Context, t core.Task, reason string) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status, t.Detail = core.TaskStopped, reason
		return t.Objective + " stopped", nil
	})
	return err
}

// settleAnswers applies the owner's answers to decisions tasks are waiting on.
func (lp *Loop) settleAnswers(ctx context.Context, snap core.Snapshot) (bool, error) {
	for _, t := range snap.Tasks {
		if t.Status != core.TaskWaiting || t.DecisionID == "" {
			continue
		}
		d, ok := findDecision(snap, t.DecisionID)
		if !ok || d.Status == core.DecisionOpen {
			continue
		}
		return true, lp.applyAnswer(ctx, t, d)
	}
	return false, nil
}

func (lp *Loop) applyAnswer(ctx context.Context, t core.Task, d core.Decision) error {
	if d.Status == core.DecisionDismissed {
		return lp.stopTask(ctx, t, "You closed it")
	}
	answer := strings.TrimSpace(d.Answer)
	// Only a choice the owner picked acts on the task. Their own words are
	// direction, even when they spell "approve" or "stop".
	chose := func(choice string) bool {
		return d.Disposition == core.DispositionChoice && answer == choice
	}
	switch {
	case chose(choiceStop):
		return lp.stopTask(ctx, t, "You stopped it")
	case d.Approves() && chose(choiceApprove),
		d.Kind == core.DecisionEscalation && chose(choiceAcceptDraft):
		return lp.approve(ctx, t)
	case d.Kind == core.DecisionFailure && chose(choiceTryAgain):
		_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
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
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if d.Kind == core.DecisionQuestion || (!chose(choiceAnotherRound) && !chose(choiceChanges)) {
			t.AddDirection(&d, answer)
		}
		t.NextRound()
		t.Status, t.DecisionID, t.Detail = core.TaskWriting, "", "Revising with your answer"
		return fmt.Sprintf("Revising %s with your direction", t.Objective), nil
	})
	return err
}

// holdForUsage waits a task out while the role's subscription is past the
// owner's threshold. A hold is a wait, not a failure: nothing is retried or
// counted against the task, and it resumes by itself when the window resets
// or the threshold is raised.
func (lp *Loop) holdForUsage(ctx context.Context, t core.Task, r core.Role) (bool, error) {
	wait, detail := lp.usageWait(ctx, r)
	if wait.IsZero() {
		return false, nil
	}
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.RetryAt, t.Detail = wait, detail
		return "", nil
	})
	return true, err
}

// usageWait is when the role may run again, and why, while its subscription
// is past the owner's threshold; zero when it may run now.
func (lp *Loop) usageWait(ctx context.Context, r core.Role) (time.Time, string) {
	cfg := lp.Config()
	threshold, supported := quota.Threshold(cfg.Limits.RoleUsage, r.Engine)
	if !supported || threshold == 0 {
		return time.Time{}, ""
	}
	model := cfg.Model
	model.Engine, model.Model = r.Engine, r.Model
	now := time.Now()
	verdict := quota.Evaluate(lp.meter.Read(ctx, model), model, threshold, now)
	switch {
	case verdict.Held:
		wait := verdict.ResetsAt
		if !wait.After(now) {
			wait = now.Add(10 * time.Minute)
		}
		return wait, fmt.Sprintf("Waiting for %s usage to reset (%s)", engineName(r.Engine), verdict.Detail)
	case !verdict.Known && cfg.Limits.RoleUsage.OnUnavailable == "pause":
		return now.Add(5 * time.Minute), "Waiting until " + engineName(r.Engine) + " usage can be checked"
	}
	return time.Time{}, ""
}

var outcomeWords = map[string]string{core.VerdictPass: "passed", core.VerdictRevise: "asked for changes", core.VerdictQuestion: "asked a question"}

func engineName(engine string) string {
	if engine == "codex" {
		return "Codex"
	}
	if engine == "claude" {
		return "Claude"
	}
	return engine
}

// StopTask ends a task at the owner's request. A turn already running
// finishes, but nothing it reports can restart the task, and any decision the
// task was waiting on is closed as no longer needed.
func (lp *Loop) StopTask(ctx context.Context, projectID, taskID string) (core.Task, error) {
	var decisionID string
	stopped, err := lp.Core.UpdateTask(ctx, taskID, func(t *core.Task, _ *core.Project) (string, error) {
		if t.ProjectID != projectID {
			return "", core.ErrNotFound
		}
		if t.Finished() {
			return "", fmt.Errorf("this request has already finished: %w", core.ErrConflict)
		}
		decisionID = t.DecisionID
		t.Status, t.DecisionID, t.Detail = core.TaskStopped, "", "You stopped it"
		return t.Objective + " stopped", nil
	})
	if err != nil {
		return stopped, err
	}
	if decisionID != "" {
		if _, dismissErr := lp.Core.DismissDecision(ctx, decisionID, "The task was stopped"); dismissErr != nil && !errors.Is(dismissErr, core.ErrConflict) {
			return stopped, dismissErr
		}
	}
	lp.Nudge()
	return stopped, nil
}
