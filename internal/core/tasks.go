package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Task is one outcome worked through the loop. Its roles and round limit are
// copied from the playbook when it starts, so a playbook change never reshapes
// work already under way.
type Task struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// Number is the task's place in its project's creation order, from
	// which its readable ID is made; see refs.go.
	Number int `json:"number,omitempty"`
	// Ref is the task's readable ID, such as CA-12: its project's prefix and
	// its number. Derived with Stage, so it follows a renamed prefix, and
	// never stored.
	Ref       string   `json:"ref,omitempty"`
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
	Status    string   `json:"status"`
	// Stage is where the task sits on its project's board, derived from the
	// rest of the record; see stage.go.
	Stage string `json:"stage,omitempty"`
	// Place is the working stage the task holds towards its project's stage
	// limits: the stage it last entered, which it keeps while it waits for
	// room in the next. Kept by Schedule; see stage_limits.go.
	Place string `json:"place,omitempty"`
	// Checking names who is at work in a stage someone else leads: the
	// checker while the task is checked, the researcher while it is
	// researched, the designer while it is with the designer, the PM while
	// it is in triage. Derived with Stage.
	Checking string `json:"checking,omitempty"`
	// WithDesigner is a task handed to the designer for design input, which
	// stays in the stage of the role that handed it over. Derived with Stage.
	WithDesigner bool `json:"with_designer,omitempty"`
	// Answered is a task waiting on a decision the owner has already made:
	// the loop takes the answer at its next step, so it no longer needs
	// the owner. Derived with Stage.
	Answered bool `json:"answered,omitempty"`
	// WaitsFor names the unfinished tasks this one depends on, derived with
	// Stage, so the board can say what it waits for.
	WaitsFor []string `json:"waits_for,omitempty"`
	// PMDeciding is a signed-off change waiting only on the PM's decision
	// to land, which the owner may take ahead of it. Derived with Stage.
	PMDeciding bool   `json:"pm_deciding,omitempty"`
	Detail     string `json:"detail,omitempty"`
	// Waiting is who or what the task's next step waits for, while it is
	// ready and can't start; see Schedule.
	Waiting *Wait  `json:"waiting,omitempty"`
	Roles   []Role `json:"roles,omitempty"`
	// Playbook is the team's setup as it was when the task started: its
	// medium and, for code, the repository, check and branch prefix.
	Playbook  *Playbook `json:"playbook,omitempty"`
	MaxRounds int       `json:"max_rounds,omitempty"`
	Round     int       `json:"round"`
	// Direction is what the owner asked for along the way, in their words.
	// DirectionPending counts the entries the implementer has not yet had in
	// view: until it has, the task goes back to the implementer rather than
	// on to approval or landing.
	Direction        []string `json:"direction,omitempty"`
	DirectionPending int      `json:"direction_pending,omitempty"`
	// Messages is what the owner or assistant said directly to a member of
	// the team, with their replies.
	Messages  []TeamMessage `json:"messages,omitempty"`
	Revisions []Revision    `json:"revisions"`
	// Handoff is a revision on its way to being recorded; see handoff.go.
	// Attempt counts the handoffs the task has started, and only goes up.
	Handoff *Handoff `json:"handoff,omitempty"`
	Attempt int      `json:"attempt,omitempty"`
	// Claims are the steps of the task seats have taken and not yet
	// finished; see claims.go.
	Claims []Claim `json:"claims,omitempty"`
	// Beside names the project's tasks that were under way at the same
	// time as this one, which it was built beside rather than after.
	Beside []string `json:"beside,omitempty"`
	// Delivering is the revision a landing is taking where it lands, set
	// before the push or merge and cleared once its outcome is recorded; a
	// task stopped or restarted meanwhile is settled from where it went.
	Delivering *Delivering `json:"delivering,omitempty"`
	// Plan is what the researcher worked out before anything was written. It
	// is kept on the task, so everyone who works on it reads the same plan
	// rather than inheriting a conversation.
	Plan *Plan `json:"plan,omitempty"`
	// Design is each time the researcher or the implementer handed the task
	// to the designer, with the input it gave; see design.go.
	Design []DesignRequest `json:"design,omitempty"`
	// CurrentDesign is the request whose input is the task's current
	// design: the target everyone works and judges to. Earlier designs stay
	// on the record as superseded; see design.go.
	CurrentDesign string `json:"current_design,omitempty"`
	// Research is each time a checker sent the task back to the researcher
	// for more research; see routing.go.
	Research []ResearchRequest `json:"research,omitempty"`
	// Asker is who asked the question the task waits on the owner for, so
	// the answer goes back to them; see routing.go.
	Asker *Asker `json:"asker,omitempty"`
	// Edits are the changes the team or the owner made to the objective and
	// criteria after the task was asked for, oldest first; see edits.go.
	Edits []TaskEdit `json:"edits,omitempty"`
	// TextVersion counts the edits to the objective and criteria. A verdict
	// counts only against the text it judged, so a change to what the task
	// asks for has every checker judge again.
	TextVersion int `json:"text_version,omitempty"`
	// Notes are what the team, the owner and the assistant left on the task
	// for each other: a shared channel beside the record, which never takes
	// its place; see notes.go.
	Notes []Note `json:"notes,omitempty"`
	// Attachments are the files kept with the task: the owner's, with a
	// note, and the designer's, with a design; see attachments.go.
	Attachments []Attachment `json:"attachments,omitempty"`
	// DependsOn names tasks in the same project that must have landed before
	// this one starts. Without stacking, a task never builds on work that has
	// not landed.
	DependsOn []string `json:"depends_on,omitempty"`
	// RelatesTo names tasks in the same project worth looking at alongside
	// this one. It is kept on both tasks.
	RelatesTo []string `json:"relates_to,omitempty"`
	// LinkedBy records who set each of this task's links, keyed by relation
	// and task; see links.go.
	LinkedBy map[string]LinkMark `json:"linked_by,omitempty"`
	// Blockers keep external conditions and their clearing history.
	Blockers []Blocker `json:"blockers,omitempty"`
	// SplitFrom is the task this one was split off from by that task's
	// researcher, which a later plan for it matches rather than queues again;
	// see plans.go.
	SplitFrom string `json:"split_from,omitempty"`
	// Blocks names the tasks that depend on this one. Derived with Stage.
	Blocks   []string  `json:"blocks,omitempty"`
	Verdicts []Verdict `json:"verdicts"`
	// Threads are the team members' conversations on this task, one per
	// member and kind of role; see threads.go. Only the implementer resumes
	// one across rounds. Reviewers always start fresh, so no earlier
	// judgement anchors the next.
	Threads []Thread `json:"threads,omitempty"`
	// WriterNext is what to do with the implementer's thread at its next
	// round: WriterCompact or WriterFresh, or empty to resume it as it is.
	// WriterRequest numbers each request, so a round clears only the one it
	// carried out and not the same request made again while it ran.
	WriterNext    string `json:"writer_next,omitempty"`
	WriterRequest int    `json:"writer_request,omitempty"`
	Failures      int    `json:"failures,omitempty"`
	// ResumeStatus is the step to return to when an owner decision about a
	// failure is answered with a retry.
	ResumeStatus string    `json:"resume_status,omitempty"`
	RetryAt      time.Time `json:"retry_at,omitempty"`
	// HeldFor is the engine whose usage limit set RetryAt, when that is why
	// the task waits; the loop looks again sooner than RetryAt, since the
	// limit can be raised and usage can fall.
	HeldFor    string `json:"held_for,omitempty"`
	DecisionID string `json:"decision_id,omitempty"`
	// Base, From and Branch record the commit a code task started from, the
	// owner's branch it was on, and the branch its revisions are committed to.
	Base        string `json:"base,omitempty"`
	From        string `json:"from,omitempty"`
	Branch      string `json:"branch,omitempty"`
	DeliveredTo string `json:"delivered_to,omitempty"`
	// CatchUps counts catch-ups while landing, so a target that keeps moving
	// comes to the owner instead of looping.
	CatchUps int `json:"catch_ups,omitempty"`
	// WakeErrors are problems with the implementer's last wake block, shown to
	// it in its next round.
	WakeErrors []string `json:"wake_errors,omitempty"`
	// Approved is the revision the owner approved to land. A revision that
	// only merged it cleanly with landed work keeps that approval.
	Approved int `json:"approved,omitempty"`
	// LandDecision is the PM's latest decision to land or hold the change,
	// on a project where the PM decides; see landing.go.
	LandDecision *LandDecision `json:"land_decision,omitempty"`
	// LandingFailures are why landings the PM approved failed since the
	// change last landed or the owner stepped in: past a few, the owner
	// decides instead.
	LandingFailures []string `json:"landing_failures,omitempty"`
	// Unreachable are the requirements the implementer said it can't meet
	// from its sandbox, with the draft it said so of, for the PM or the
	// owner to judge; OwnerSteps are those the owner took on, to check once
	// the change lands. See owner_steps.go.
	Unreachable []Unreachable `json:"unreachable,omitempty"`
	OwnerSteps  []string      `json:"owner_steps,omitempty"`
	// OwnerTook is each requirement the owner took on as one of those
	// steps, as the task or its brief states it. One of the brief's stays
	// in the brief, and is the owner's for this task alone.
	OwnerTook []string `json:"owner_took,omitempty"`
	// Proposal is the pull request a task lands through, and the branch the
	// project owns for it.
	Proposal  *Proposal `json:"proposal,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// StartedAt is when the task first left the to-do list.
	StartedAt time.Time `json:"started_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Task statuses. Writing, reviewing and deciding are the loop's own; waiting
// means an owner decision is open; the rest are final.
const (
	TaskQueued = "queued"
	// TaskTriage is a task the owner or the assistant asked for, with the
	// PM to shape before it joins the to-do list; see pm.go. It never
	// starts from here.
	TaskTriage  = "triage"
	TaskWriting = "writing"
	// TaskResearching is the researcher working out what the task needs,
	// before anything is written. Older state calls it planning.
	TaskResearching = "researching"
	// TaskDesigning is the designer giving the design input the researcher
	// or the implementer asked for; the task then goes back to whichever
	// asked.
	TaskDesigning = "designing"
	TaskReviewing = "reviewing"
	TaskDeciding  = "deciding"
	TaskWaiting   = "waiting"
	TaskDelivered = "delivered"
	TaskStopped   = "stopped"
	// TaskLanding is landing an approved change; TaskAwaiting is waiting, idle,
	// for something outside the team (CI, a review) before it can go on.
	TaskLanding  = "landing"
	TaskAwaiting = "awaiting"
	TaskLanded   = "landed"
)

func (t Task) Active() bool {
	_, ok := progress[t.Status]
	return ok
}

// Finished reports a task that will do nothing more on its own.
func (t Task) Finished() bool {
	return t.Status == TaskDelivered || t.Status == TaskLanded || t.Status == TaskStopped
}

// Proposal is a task's pull request. Branch is owned by the project: it is
// only ever updated with a lease on Pushed, the last commit pushed there.
type Proposal struct {
	Branch string `json:"branch"`
	Pushed string `json:"pushed,omitempty"`
	Number int    `json:"number,omitempty"`
	URL    string `json:"url,omitempty"`
	// Seen is the newest review or comment already passed to the team, and
	// ChecksFor the commit whose failing checks were.
	Seen      time.Time `json:"seen,omitzero"`
	ChecksFor string    `json:"checks_for,omitempty"`
	// Title and Body are the pull request's, as the implementer wrote them
	// with its latest draft, and Described what GitHub was last given.
	Title     string `json:"title,omitempty"`
	Body      string `json:"body,omitempty"`
	Described string `json:"described,omitempty"`
	// PushedAt is when Pushed went up, so checks that haven't started yet
	// aren't taken for none at all.
	PushedAt time.Time `json:"pushed_at,omitzero"`
	// Observed is what the loop last saw of the open pull request.
	Observed *Observed `json:"observed,omitempty"`
	// Answering is an implementer's round on what the pull request asked
	// for; it ends when the task leaves writing.
	Answering bool `json:"answering,omitempty"`
	// MergeApproved is the revision approved to merge once its pull request
	// is ready, by the owner or the PM: apart from Task.Approved, which
	// approved it opening.
	MergeApproved int `json:"merge_approved,omitempty"`
	// Outbox is what the team has to say on the pull request, posted once
	// the revision it came with has been pushed.
	Outbox []PRPost `json:"outbox,omitempty"`
}

// PRPost is a reply on a pull request: in a review thread, or with Thread
// empty, in its conversation; or with Resolve, a thread marked resolved.
// By is the seat it is from.
type PRPost struct {
	Thread  string `json:"thread,omitempty"`
	Body    string `json:"body,omitempty"`
	Resolve bool   `json:"resolve,omitempty"`
	By      string `json:"by"`
}

// Post queues replies for the task's pull request.
func (t *Task) Post(posts ...PRPost) {
	if len(posts) == 0 {
		return
	}
	if t.Proposal == nil {
		t.Proposal = &Proposal{}
	}
	t.Proposal.Outbox = append(t.Proposal.Outbox, posts...)
}

// Observed is the state of an open pull request as the loop last saw it.
type Observed struct {
	// Checks is SUCCESS, FAILURE, PENDING or NONE.
	Checks string `json:"checks"`
	// Review is GitHub's review decision, empty where the repository asks
	// for none.
	Review     string `json:"review,omitempty"`
	Unresolved int    `json:"unresolved,omitempty"`
	// Conflicting is a pull request that can't merge into its base as it is.
	Conflicting bool `json:"conflicting,omitempty"`
	// Ready is approved where review is asked for, green, every thread
	// resolved and mergeable: ready to land.
	Ready bool `json:"ready,omitempty"`
	// Ignored counts feedback from people outside the repository, which
	// the team never acts on.
	Ignored int       `json:"ignored,omitempty"`
	At      time.Time `json:"at"`
}

// PRText is a pull request's title and description.
type PRText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Describe keeps the pull request text the implementer wrote with a draft.
func (t *Task) Describe(text PRText) {
	if t.Proposal == nil {
		t.Proposal = &Proposal{}
	}
	t.Proposal.Title, t.Proposal.Body = text.Title, text.Body
}

// PROpen reports a task with a pull request open for it.
func (t Task) PROpen() bool { return t.Proposal != nil && t.Proposal.Number > 0 }

// UsesPRs reports a task that lands through a pull request, as its team had
// it when it started.
func (t Task) UsesPRs() bool { return t.Playbook != nil && t.Playbook.Land.PullRequests }

// Delivering is a landing under way: the revision going out, and when.
type Delivering struct {
	Revision int       `json:"revision"`
	At       time.Time `json:"at"`
}

// Revision is one snapshot of the artifact, stamped with the brief it answers.
type Revision struct {
	N            int      `json:"n"`
	BriefVersion int      `json:"brief_version"`
	Files        []string `json:"files"`
	// CleanMergeOf is the revision this one merged, unchanged, with work that
	// landed since. The daemon made it; the task's own change is the same.
	CleanMergeOf int `json:"clean_merge_of,omitempty"`
	// Ref identifies the revision in its medium, such as a commit.
	Ref     string    `json:"ref,omitempty"`
	Summary string    `json:"summary,omitempty"`
	At      time.Time `json:"at"`
	// By is who made the revision when the team didn't: DraftByOwner for a
	// change the owner made by hand.
	By string `json:"by,omitempty"`
}

// Verdict is one reviewer's judgement of one revision.
type Verdict struct {
	Revision int `json:"revision"`
	// Ref is exactly what was checked: the commit, or the digest of a
	// document draft, in the checker's own copy of the revision; for pull
	// request feedback, the commit GitHub says it was on. It is empty only
	// for a pull request comment, which is on no commit.
	Ref          string    `json:"ref,omitempty"`
	Role         string    `json:"role"`
	BriefVersion int       `json:"brief_version"`
	Outcome      string    `json:"outcome"`
	Summary      string    `json:"summary"`
	Findings     []Finding `json:"findings,omitempty"`
	Question     string    `json:"question,omitempty"`
	// Asked is the message this check answered, when someone asked for it.
	Asked string `json:"asked,omitempty"`
	// Outside marks feedback from outside the team, such as a pull request
	// review: to be weighed on its merits, never followed as instructions.
	Outside bool `json:"outside,omitempty"`
	// Next is where the checker recommends the task goes next, and Note a
	// line to go with it, such as "I want to see it again"; see routing.go.
	Next string `json:"next,omitempty"`
	Note string `json:"note,omitempty"`
	// Answered marks a question or research request whose answer came back:
	// the checker judges the revision again with it, so this verdict no
	// longer counts.
	Answered bool `json:"answered,omitempty"`
	// TextVersion is the task's TextVersion the checker was shown: a verdict
	// on objective and criteria that have since changed no longer counts.
	TextVersion int `json:"text_version,omitempty"`
	// ID names a verdict that kept screenshots, which are attached to the
	// task with it as their origin.
	ID string `json:"id,omitempty"`
	// Evidence is what QA saw using the app: screenshots, and what it found
	// in the page, its console and its network requests.
	Evidence []Evidence `json:"evidence,omitempty"`
	At       time.Time  `json:"at"`
}

// Evidence is one thing QA saw while it used the app: a screenshot, kept as
// one of the task's attachments, or a finding in words.
type Evidence struct {
	Kind       string `json:"kind"`
	Text       string `json:"text,omitempty"`
	Attachment string `json:"attachment,omitempty"`
}

// Kinds of evidence, and how much of it a verdict keeps.
const (
	EvidenceConsole    = "console"
	EvidenceNetwork    = "network"
	EvidencePage       = "page"
	EvidenceScreenshot = "screenshot"
	// MaxEvidence is how many findings in words a verdict keeps, and
	// MaxEvidenceText how long each can be; MaxScreenshots is how many of
	// the turn's screenshots it keeps, the last ones taken.
	MaxEvidence     = 12
	MaxEvidenceText = 1000
	MaxScreenshots  = 4
)

const (
	VerdictPass     = "pass"
	VerdictRevise   = "revise"
	VerdictQuestion = "question"
	// VerdictResearch sends the task back to the researcher with the
	// checker's question, then back to the checker.
	VerdictResearch = "research"
)

type Finding struct {
	Criterion string `json:"criterion,omitempty"`
	Note      string `json:"note"`
}

type TaskInput struct {
	Objective string   `json:"objective"`
	Criteria  []string `json:"criteria"`
	DependsOn []string `json:"depends_on,omitempty"`
}

// task is the task id names: its canonical ID, or failing that its
// readable ID.
func task(v *Snapshot, id string) *Task {
	for i := range v.Tasks {
		if v.Tasks[i].ID == id {
			return &v.Tasks[i]
		}
	}
	return taskByRef(v, id)
}

func decision(v *Snapshot, id string) *Decision {
	for i := range v.Decisions {
		if v.Decisions[i].ID == id {
			return &v.Decisions[i]
		}
	}
	return nil
}

// QueueTask asks for an outcome, as the owner. It starts when the loop
// reaches it.
func (s *Service) QueueTask(ctx context.Context, projectID string, in TaskInput) (Task, error) {
	return s.QueueTaskAs(ctx, projectID, in, LinkedByOwner)
}

// QueueTaskAs asks for an outcome on behalf of by, who holds what it says
// the task waits for. What the owner or the assistant asks for goes to the
// project's PM for triage first, when it has one; what the team asks for
// joins the to-do list directly.
func (s *Service) QueueTaskAs(ctx context.Context, projectID string, in TaskInput, by string) (Task, error) {
	if !required(in.Objective) {
		return Task{}, errors.New("a task needs an objective")
	}
	now := s.now().UTC()
	out := Task{ID: uid(), ProjectID: projectID, Objective: strings.TrimSpace(in.Objective), Criteria: cleanList(in.Criteria), Status: TaskQueued, Stage: StageTodo, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now}
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if p.Playbook == nil {
			return errors.New("choose how this project's work gets done before asking for it")
		}
		if !required(p.Brief.Goal) {
			return errors.New("give the project a brief before asking for work")
		}
		deps, err := dependencies(v, out, in.DependsOn)
		if err != nil {
			return err
		}
		out.DependsOn = deps
		markAll(&out, deps, by, now)
		numberTask(p, &out)
		if _, pm := p.PMSeat(); pm && overrules(by) {
			out.Status, out.Stage = TaskTriage, StageTriage
		}
		v.Tasks = append(v.Tasks, out)
		p.listChanged()
		recordTask(v, now, &out, "task."+out.Status, out.Objective)
		return nil
	})
	return out, err
}

// OrderTasks sets the order a project's queued tasks start in. ids must be
// exactly the project's queued tasks, so one that started or arrived since
// the owner or assistant looked is never silently left out. Other projects'
// tasks keep their places.
func (s *Service) OrderTasks(ctx context.Context, projectID string, ids []string, by string) ([]Task, error) {
	var out []Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		ordered, err := reorder(v, projectID, ids)
		if err != nil {
			return err
		}
		out = ordered
		now := s.now().UTC()
		p.OrderedBy, p.OrderedAt = by, now
		record(v, now, projectID, "task.reordered", "To-do list reordered")
		return nil
	})
	return out, err
}

// reorder puts a project's queued tasks in the order of ids, which must be
// exactly those tasks, keeping the slots they held among other projects'.
func reorder(v *Snapshot, projectID string, ids []string) ([]Task, error) {
	var slots []int
	queued := map[string]Task{}
	for i, t := range v.Tasks {
		if t.ProjectID == projectID && t.Status == TaskQueued {
			slots = append(slots, i)
			queued[t.ID] = t
		}
	}
	changed := fmt.Errorf("the to-do list changed; try again: %w", ErrConflict)
	if len(ids) != len(slots) {
		return nil, changed
	}
	var out []Task
	for _, id := range canonicalIDs(v, ids) {
		t, ok := queued[id]
		if !ok {
			return nil, changed
		}
		delete(queued, id)
		out = append(out, t)
	}
	for i, slot := range slots {
		v.Tasks[slot] = out[i]
	}
	return out, nil
}

// UpdateTask applies one loop transition atomically. The loop decides what
// happens next; the store makes sure it happens to the current record.
func (s *Service) UpdateTask(ctx context.Context, id string, fn func(*Task, *Project) (activity string, err error)) (Task, error) {
	return s.updateTask(ctx, id, func(_ *Snapshot, t *Task, p *Project) (string, error) {
		return fn(t, p)
	})
}

// UpdateTaskWithVerdict is UpdateTask for a transition that may record a
// check's verdict, with the screenshots the check took. fn gets the verdict
// with its screenshots as evidence, to record or not. The screenshots are
// kept in the same update, and only if the verdict fn was given is among
// the task's verdicts afterwards; otherwise, or if the update fails, the
// files written for them are removed.
func (s *Service) UpdateTaskWithVerdict(ctx context.Context, id string, verdict Verdict, shots Screenshots, fn func(t *Task, p *Project, verdict Verdict) (activity string, err error)) (Task, error) {
	pending := s.writeScreenshots(ctx, id, shots)
	out, err := s.updateTask(ctx, id, func(v *Snapshot, t *Task, p *Project) (string, error) {
		now := s.now().UTC()
		recorded := verdict
		recorded.Evidence = slices.Clone(verdict.Evidence)
		pending.evidence(t, &recorded, now)
		activity, err := fn(t, p, recorded)
		if err != nil {
			return "", err
		}
		pending.keep(v, t, recorded.ID, now)
		return activity, nil
	})
	pending.done(err)
	return out, err
}

func (s *Service) updateTask(ctx context.Context, id string, fn func(*Snapshot, *Task, *Project) (activity string, err error)) (Task, error) {
	var out Task
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, id)
		if t == nil {
			return ErrNotFound
		}
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		wasFinished := t.Finished()
		activity, err := fn(v, t, p)
		if err != nil {
			return err
		}
		t.UpdatedAt = s.now().UTC()
		if t.Status != TaskWriting && t.Proposal != nil {
			t.Proposal.Answering = false
		}
		if t.Finished() {
			cancelTaskWakes(v, t.ID)
			closeMessages(t, t.UpdatedAt)
			if !wasFinished {
				p.listChanged()
			}
		}
		// A delivered task may still land later and resume its writer, so it
		// keeps what its roles were told; one stopped or landed never runs again.
		if t.Status == TaskStopped || t.Status == TaskLanded {
			for i := range t.Roles {
				t.Roles[i].Learnings = nil
			}
		}
		// Stopping a task takes back every step it holds: its seats are
		// free, and whatever its turns return late records nothing.
		if t.Status == TaskStopped {
			t.Claims = nil
		}
		if activity != "" {
			recordTask(v, t.UpdatedAt, t, "task."+t.Status, activity)
		}
		derive(v, t)
		out = *t
		return nil
	})
	return out, err
}

// OpenTaskDecision puts a choice about a task in front of the owner and holds
// the task until it is answered.
func (s *Service) OpenTaskDecision(ctx context.Context, taskID, kind string, in DecisionInput) (Decision, error) {
	if err := in.validTaskDecision(); err != nil {
		return Decision{}, err
	}
	var out Decision
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		out = openTaskDecision(v, t, kind, in, s.now().UTC())
		return nil
	})
	return out, err
}

func (in DecisionInput) validTaskDecision() error {
	if !required(in.Title, in.Context, in.Recommendation) || len(in.Choices) < 2 {
		return errors.New("decision requires title, context, recommendation and at least two choices")
	}
	return nil
}

// openTaskDecision holds a task for a decision, within a change.
func openTaskDecision(v *Snapshot, t *Task, kind string, in DecisionInput, now time.Time) Decision {
	d := Decision{ID: uid(), Kind: kind, TaskID: t.ID, ProjectID: t.ProjectID, Title: in.Title, Context: in.Context, Recommendation: in.Recommendation, Choices: in.Choices, FollowUp: in.FollowUp, OwnerStep: in.OwnerStep, Status: DecisionOpen, CreatedAt: now}
	t.Status = TaskWaiting
	t.DecisionID = d.ID
	t.UpdatedAt = now
	v.Decisions = append(v.Decisions, d)
	recordTask(v, now, t, "decision.opened", in.Title)
	return d
}
