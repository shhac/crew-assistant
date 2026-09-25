// Package work runs a project's team: the loop that takes each task through
// writing, checking, the owner's approval and landing, and the watcher that
// wakes agents when what they wait on changes. The assistant and the
// dashboard drive it through Loop's methods.
package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/github"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
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
// restart resumes at the step it was on. A step is taken only while
// stop.Graceful lasts and runs on stop.Force, so a stop lets the step in
// progress finish and starts no other.
func (lp *Loop) Run(stop lifecycle.Stop, noDispatch bool) {
	// Learnings are copied out only while a turn runs; any left here were
	// left by a daemon that stopped mid-turn.
	os.RemoveAll(lp.learningsRoot())
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for !stop.Stopping() {
			progressed, err := lp.loopStep(stop.Force, noDispatch)
			if err != nil && stop.Force.Err() == nil {
				lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_loop"}, err)
			}
			if !progressed || err != nil {
				break
			}
		}
		select {
		case <-stop.Graceful.Done():
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
	if progressed, err := lp.managePM(ctx, snap); progressed || err != nil {
		return progressed, err
	}
	if err := lp.releaseUsageHolds(ctx, snap); err != nil {
		return false, err
	}
	t, ok, err := lp.Core.NextTask(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
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
	case core.TaskResearching:
		return true, lp.researchTask(ctx, p, t, m)
	case core.TaskDesigning:
		return true, lp.design(ctx, p, t, m)
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

// roleSpec is how a role runs for one turn. The files it reads its learnings
// from last only as long as the turn: run it before cleanup.
func (lp *Loop) roleSpec(t core.Task, r core.Role, workDir string, write bool, m medium, prompt string) (spec roles.Spec, cleanup func(), err error) {
	spec = lp.baseSpec(r, workDir, prompt)
	spec.Write, spec.Env, spec.Read = write, m.env(), m.readable()
	learned, err := lp.prepareLearnings(t, r)
	if err != nil {
		return spec, nil, err
	}
	if learned.index != "" {
		spec.Read = append(append([]string(nil), spec.Read...), learned.dir)
		spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + learned.index)
	}
	return spec, learned.cleanup, nil
}

// baseSpec is a read-only turn for a role: its engine, the login that
// engine uses, its instructions and the prompt.
func (lp *Loop) baseSpec(r core.Role, workDir, prompt string) roles.Spec {
	spec := roles.Spec{Engine: r.Engine, Model: r.Model, Effort: r.Effort, WorkDir: workDir, Instructions: r.Instructions, Prompt: prompt}
	spec.Binary, spec.Home = lp.Config().Engines.Binary(r.Engine)
	if r.Engine == "codex" {
		spec.RuntimeHome = filepath.Join(lp.Core.StateDirectory(), "roles", "codex")
	}
	return spec
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
