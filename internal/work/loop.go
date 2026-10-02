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

	"github.com/shhac/lib-agent-harness/session"

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
	// choiceAcceptFollowUp accepts the draft and queues what the checks
	// still raise as a follow-up task.
	choiceAcceptFollowUp = "Accept and follow up"
	// choiceOwnerStep leaves a requirement the team can't meet from its
	// sandbox to the owner after the change lands; choiceKeepForTeam keeps
	// it the team's.
	choiceOwnerStep   = "Make it an owner step"
	choiceKeepForTeam = "Keep it for the team"
	choiceStop        = "Stop"
	choiceTryAgain    = "Try again"
	choiceResolve     = "Let the implementer resolve it"
	// A role that fails is retried this many times, with growing waits,
	// before the owner hears about it.
	roleRetries = 2
)

// Loop runs the teams' tasks. Steps run side by side, each claimed by a
// seat that takes one step at a time, within each project's cap on tasks
// under way and each engine's bound on role turns at once. Each task's work
// is in a workspace of its own, and each check in a copy of the revision of
// its own.
type Loop struct {
	Core   *core.Service
	Config func() config.Config
	// Diagnostics is set before the loop starts.
	Diagnostics *diagnostics.Logger
	Demo        bool
	runner      roles.Runner
	meter       *quota.Meter
	keptUsage   usageStore
	// github reads and merges pull requests; githubURL is where git pushes.
	// Both are replaced in tests.
	github    github.Client
	githubURL func(repo string) string
	prSeen    sync.Map
	// posting is held while the team's replies go up on a pull request, by
	// the landing step or by a teammate answering beside it.
	posting sync.Mutex
	// Build and Includes are injectable read-only observations of this daemon.
	Build         func() (revision string, ok bool)
	Includes      func(context.Context, string, string, string, string) (bool, error)
	blockerChecks sync.Map
	loopWake      chan struct{}
	turns         turnRegister
	// gate bounds role turns per engine and holds them back while the owner
	// is busy; jobs are the claimed steps running now.
	gate gate
	jobs jobs
	// learnedUse counts the turns reading each folder of learnings.
	learnedMu  sync.Mutex
	learnedUse map[string]int
	// reclaim ends a turn a stopped daemon left running; empty is the
	// harness's own. Replaced in tests.
	reclaim func(ctx context.Context, dir string) (session.Reclamation, error)
	// ports are held by QA checks that run the app, one each.
	ports ports
	// checked, when set, is told a checker's turn is over, before its
	// verdict is recorded. Set in tests.
	checked func(taskID, checker string)
	// slept replaces how long the machine slept since a time. Set in tests.
	slept func(start time.Time) time.Duration
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

// Run works tasks, several steps at once. Each step is one role turn or one
// state transition, claimed before it starts and recorded before its claim
// is cleared, so a restart resumes each task at the step it was on without
// running a step twice. A step is started only while stop.Graceful lasts and
// runs on stop.Force, so a stop lets the steps in progress finish and starts
// no other.
func (lp *Loop) Run(stop lifecycle.Stop, noDispatch bool) {
	// Learnings are copied out only while a turn runs; any left here were
	// left by a daemon that stopped mid-turn.
	os.RemoveAll(lp.learningsRoot())
	if !lp.Demo && !noDispatch {
		if err := lp.resume(stop.Force); err != nil {
			lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_resume"}, err)
		}
	}
	defer lp.jobs.wg.Wait()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		for !stop.Stopping() && !lp.Demo && !noDispatch {
			progressed, _, err := lp.pass(stop.Force, false)
			if err != nil && stop.Force.Err() == nil {
				lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_loop"}, err)
			}
			if !progressed || err != nil {
				break
			}
		}
		// With nothing more to start, what finished tasks kept goes.
		if !lp.Demo && !noDispatch && !stop.Stopping() {
			if err := lp.tidy(stop.Force, false); err != nil {
				lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "task_cleanup"}, err)
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

// loopStep is one pass that waits for the steps it started to end, for
// callers that take the work a pass at a time.
func (lp *Loop) loopStep(ctx context.Context, noDispatch bool) (bool, error) {
	if lp.Demo || noDispatch {
		return false, nil
	}
	progressed, started, err := lp.pass(ctx, true)
	var crashed any
	for _, done := range started {
		if r := <-done; r != nil && crashed == nil {
			crashed = r
		}
	}
	if crashed != nil {
		panic(crashed)
	}
	return progressed, err
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
// findTask is the task taskID names, by its canonical or readable ID, in
// projectID unless that is empty.
func findTask(s core.Snapshot, projectID, taskID string) (core.Task, bool) {
	t, ok := s.FindTask(taskID)
	if !ok || (projectID != "" && t.ProjectID != projectID) {
		return core.Task{}, false
	}
	return t, true
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

// checkLoopback says whether r may bind and reach this machine's own
// addresses, as the project's check may: QA runs the check, and the
// implementer is asked to run the tests before handing over, so a check
// that needs a local server would otherwise fail it every time.
func checkLoopback(playbook *core.Playbook, r core.Role) bool {
	return playbook != nil && playbook.CheckLoopback && config.Supports(r.Engine, config.UseLoopback)
}

// roleSpec is how a role runs for one turn. The files it reads its learnings
// from last only as long as the turn: run it before cleanup.
func (lp *Loop) roleSpec(t core.Task, r core.Role, workDir string, write bool, m medium, prompt string) (spec roles.Spec, cleanup func(), err error) {
	spec = lp.baseSpec(r, workDir, prompt)
	spec.Write, spec.Env, spec.Read = write, m.env(t), m.readable()
	learned, err := lp.prepareLearnings(t, r)
	if err != nil {
		return spec, nil, err
	}
	if learned.index != "" {
		spec.Read = append(append([]string(nil), spec.Read...), learned.dir)
		spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + learned.index)
	}
	// The task's attachments are read where they are kept, never copied
	// into a workspace.
	if len(t.Attachments) > 0 {
		dir := lp.Core.AttachmentsDirectory(t.ID)
		spec.Read = append(append([]string(nil), spec.Read...), dir)
		spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + attachmentsIndex(t, dir))
	}
	kind := turnKind(t, r)
	tools := lp.toolsFor(t, kind, r)
	tools.workDir = workDir
	// The researcher, having read the repository, may propose how QA runs
	// the app.
	if g, ok := m.(gitMedium); ok && kind == core.RoleResearcher {
		tools = tools.proposing(&g.playbook)
	}
	lp.withTools(&spec, tools)
	spec.Observer = lp.watchTurn(t, kind, r, workDir, write)
	// Research is the one step that looks outward; nothing its shell runs
	// reaches the network either way.
	spec.Web = kind == core.RoleResearcher
	cleanup = learned.cleanup
	// A designer's generated images are found by the session they were made
	// in, and go once the turn is over, attached or not; those of a session
	// not confirmed gone stay until a restart reclaims it.
	if g := tools.generated; g != nil {
		spec.Opened, spec.Ended = g.opened, g.closed
		cleanup = func() {
			learned.cleanup()
			g.remove()
		}
	}
	return spec, cleanup, nil
}

// turnKind is the role a seat plays in this turn of t, which a seat holding
// several roles plays one at a time.
func turnKind(t core.Task, r core.Role) string {
	switch t.Status {
	case core.TaskResearching:
		return core.RoleResearcher
	case core.TaskDesigning:
		return core.RoleDesigner
	case core.TaskWriting:
		return core.RoleImplementer
	case core.TaskReviewing, core.TaskDeciding:
		if r.Holds(core.RoleQA) {
			return core.RoleQA
		}
		return core.RoleReviewer
	}
	// Any other step, such as QA checking a landing, is a check: research's
	// wider reach comes only with researching.
	if r.Holds(core.RoleQA) {
		return core.RoleQA
	}
	return core.RoleImplementer
}

// withTools lets a role look up its project's tasks while it works, and
// link its own task as its role may.
func (lp *Loop) withTools(spec *roles.Spec, tools roleTools) {
	spec.Tools, spec.Handler = tools.Definitions(), tools.Handler()
	spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + tools.guide())
}

// baseSpec is a read-only turn for a role: its engine, the login that
// engine uses, its instructions and the prompt, and the browser where its
// member allows one.
func (lp *Loop) baseSpec(r core.Role, workDir, prompt string) roles.Spec {
	spec := roles.Spec{Engine: r.Engine, Model: r.Model, Effort: r.Effort, WorkDir: workDir, Instructions: r.Instructions, Prompt: prompt}
	spec.Binary, spec.Home = lp.Config().Engines.Binary(r.Engine)
	spec.RuntimeHome = lp.runtimeHome(r.Engine)
	if b := lp.memberBrowser(r); b.On {
		spec.Browser = true
		spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + browserGuide(b))
	}
	return spec
}

// memberBrowser is the browser a seat's member allows. It is read as the
// turn starts, not copied into seats, so allowing or withdrawing it reaches
// every seat and task the member holds at once. A template seat, a member
// who has left and an engine whose sandboxed sessions refuse the browser
// have none.
func (lp *Loop) memberBrowser(r core.Role) core.Browser {
	if r.Member == "" || !config.Supports(r.Engine, config.UseBrowser) {
		return core.Browser{}
	}
	snap, err := lp.Core.Snapshot(context.Background())
	if err != nil {
		return core.Browser{}
	}
	m, ok := snap.Member(r.Member)
	if !ok {
		return core.Browser{}
	}
	return m.Browser
}

// browserGuide is what a member allowed the browser is told about it.
func browserGuide(b core.Browser) string {
	return roles.BrowserGuide("You can use a browser this turn; your shell reaches no network, so it can't open an app you start.", b.Name)
}

// lookUp is a project's task as the state holds it now, with the project
// and the whole state it was read from.
func (lp *Loop) lookUp(ctx context.Context, projectID, taskID string) (core.Snapshot, core.Project, core.Task, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Snapshot{}, core.Project{}, core.Task{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Snapshot{}, core.Project{}, core.Task{}, core.ErrNotFound
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return core.Snapshot{}, core.Project{}, core.Task{}, core.ErrNotFound
	}
	return snap, p, t, nil
}

// pmWorkDir is the folder the PM's turns run in, which none of them uses:
// the PM reads only what its prompt carries.
func (lp *Loop) pmWorkDir() (string, error) {
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	return dir, os.MkdirAll(dir, 0o700)
}

// runtimeHome is the private home a role's Codex session runs in.
func (lp *Loop) runtimeHome(engine string) string {
	return filepath.Join(lp.Core.StateDirectory(), "roles", engine)
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
