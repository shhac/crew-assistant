package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Claim is a step of a task a seat has taken, recorded before its turn
// starts. A seat with a claim is busy, and a task with a claim isn't offered
// that step again. Token fences the turn: everything it records is recorded
// only while its claim is still on the task, so a turn that was stopped, or
// left by a daemon that stopped, records nothing when it returns late.
type Claim struct {
	// Token is "<task id>/<attempt>", on an attempt of the claim's own.
	Token string `json:"token"`
	// Step is the task status the step is for, or StepMessage or StepAdopt.
	Step string `json:"step"`
	// Seat is the seat taking the step; empty for a step no seat takes,
	// such as deciding or landing.
	Seat string `json:"seat,omitempty"`
	// Group and Revision are the checkers and the draft a check is for.
	Group    string `json:"group,omitempty"`
	Revision int    `json:"revision,omitempty"`
	// Message is the team message a StepMessage answers, or the project
	// PM chat message a StepPMChat answers.
	Message string `json:"message,omitempty"`
	// Shared is a check, which runs beside the task's other checks of the
	// same draft; any other claim holds the task alone.
	Shared bool      `json:"shared,omitempty"`
	At     time.Time `json:"at"`
	// Held says why a turn a stopped daemon left behind could not be
	// confirmed ended: its task waits rather than run the step again.
	Held string `json:"held,omitempty"`
}

const (
	// StepMessage answers a message to a reviewer or QA with a check.
	StepMessage = "message"
	// StepAdopt is the owner handing the task a draft of their own.
	StepAdopt = "adopt"
	// StepPM is the PM's look at a project's to-do list, a step of the
	// project rather than of one task.
	StepPM = "pm"
	// StepPMQuestion is the PM answering a question the assistant put to
	// it for the owner, which changes nothing.
	StepPMQuestion = "pm-question"
	StepPMChat     = "pm-chat"
)

// ErrStale is a turn whose claim has gone: the task was stopped, or its step
// was given to another attempt. It records nothing.
var ErrStale = errors.New("this turn's claim on the task has gone")

type fenceKey struct{}

// fence is the claim a turn holds: on a task, or on a project for a step of
// the project's own.
type fence struct{ task, project, token string }

// Fenced is ctx for a turn holding claim token on a task: every change made
// with it is made only while the claim is still there.
func Fenced(ctx context.Context, taskID, token string) context.Context {
	return context.WithValue(ctx, fenceKey{}, fence{task: taskID, token: token})
}

// FencedProject is ctx for a turn holding claim token on a project, as
// Fenced is for one on a task.
func FencedProject(ctx context.Context, projectID, token string) context.Context {
	return context.WithValue(ctx, fenceKey{}, fence{project: projectID, token: token})
}

// FencedLike is ctx fenced by the same claim as from, if from has one: for
// work a turn does on another context, such as answering its tool calls.
func FencedLike(ctx, from context.Context) context.Context {
	if f, ok := from.Value(fenceKey{}).(fence); ok {
		return context.WithValue(ctx, fenceKey{}, f)
	}
	return ctx
}

// FenceOf is the claim a context was fenced with, if any, and the task it is
// on, which is empty for a project's claim.
func FenceOf(ctx context.Context) (taskID, token string, ok bool) {
	f, ok := ctx.Value(fenceKey{}).(fence)
	return f.task, f.token, ok
}

// checkFence refuses a change made for a turn whose claim has gone.
func checkFence(ctx context.Context, v *Snapshot) error {
	f, ok := ctx.Value(fenceKey{}).(fence)
	if !ok {
		return nil
	}
	if f.project != "" {
		if p := project(v, f.project); p != nil && slices.ContainsFunc(p.Claims, func(c Claim) bool { return c.Token == f.token }) {
			return nil
		}
	} else if t := task(v, f.task); t != nil && t.claim(f.token) != nil {
		return nil
	}
	return fmt.Errorf("%s: %w", f.token, ErrStale)
}

func (t *Task) claim(token string) *Claim {
	for i := range t.Claims {
		if t.Claims[i].Token == token {
			return &t.Claims[i]
		}
	}
	return nil
}

// newClaim records a claim on t, on an attempt of its own.
func newClaim(t *Task, c Claim, now time.Time) Claim {
	t.Attempt++
	c.Token, c.At = fmt.Sprintf("%s/%d", t.ID, t.Attempt), now
	t.Claims = append(t.Claims, c)
	return c
}

// alone reports whether the task has a claim that holds it alone.
func (t Task) alone() bool {
	return slices.ContainsFunc(t.Claims, func(c Claim) bool { return !c.Shared })
}

// Scheduled is a step the loop has claimed, with the task as it was claimed
// and the seat that takes it, if any.
type Scheduled struct {
	Task  Task
	Claim Claim
	Seat  Role
}

// Admit lets a seat's turn start now, taking what it needs to run, such as
// a slot on its engine. It returns "" when it does; otherwise the step is
// left for a later look, and it returns what holds the turn back:
// WaitEngineCap or WaitOwner.
type Admit func(Role) string

// Kinds of Wait: a person busy with other work, the project's cap on tasks
// under way, an engine's safety cap on role turns at once, the owner's own
// use, which no new role turn starts during, and a stage of the board full
// to its limit.
const (
	WaitMember     = "member"
	WaitProjectCap = "project_cap"
	WaitEngineCap  = "engine_cap"
	WaitOwner      = "owner"
	WaitStage      = "stage"
)

// Wait is who or what a task's next step waits for while it is ready to
// start and can't: recorded by each look at the work, and cleared once the
// step is claimed.
type Wait struct {
	Kind string `json:"kind"`
	// Seat is the busy person's seat, and Member the team member it is
	// filled from, if any.
	Seat   string `json:"seat,omitempty"`
	Member string `json:"member,omitempty"`
	// On is the readable ID of the task the person is busy on; List is the
	// title of the project whose to-do list they are busy with instead.
	On   string `json:"on,omitempty"`
	List string `json:"list,omitempty"`
	// Active and Cap are the project's tasks under way and its cap on them.
	Active int `json:"active,omitempty"`
	Cap    int `json:"cap,omitempty"`
	// Engine is the engine whose safety cap is reached.
	Engine string `json:"engine,omitempty"`
	// Stage is the stage the task waits for room in, From the stage it
	// holds meanwhile, done with it, or none from To do, and Count and
	// Limit the tasks Stage holds and its limit.
	Stage string `json:"stage,omitempty"`
	From  string `json:"from,omitempty"`
	Count int    `json:"count,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// Schedule claims every step that can start now, in one change: the next
// steps of started tasks first, furthest along first, then queued tasks in
// queue order while their project is below its cap on active tasks. A person
// takes one step at a time, across every project and seat they hold. A
// task's checks each take a seat of their own, side by side; any other step
// holds the task alone, and a project lands one task at a time. A task
// enters a stage of the board only while the stage is below its limit; see
// stage_limits.go. A task whose next step is ready and can't start records
// what it waits for.
func (s *Service) Schedule(ctx context.Context, admit Admit) ([]Scheduled, error) {
	var out []Scheduled
	err := s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		busy := busyPeople(v)
		waits := map[string]*Wait{}
		stages := holdings(v)
		var active []*Task
		for i := range v.Tasks {
			if v.Tasks[i].Active() && !v.ProjectPaused(v.Tasks[i].ProjectID) {
				active = append(active, &v.Tasks[i])
			}
		}
		slices.SortStableFunc(active, func(a, b *Task) int {
			if ahead(*a, *b, now) {
				return -1
			}
			if ahead(*b, *a, now) {
				return 1
			}
			return 0
		})
		for _, t := range active {
			if t.RetryAt.After(now) || t.Handoff != nil || t.alone() {
				continue
			}
			// Done with its stage, it waits there for room in the next.
			next, full := stages.entry(v, t)
			if full != nil {
				waits[t.ID] = full
				continue
			}
			claimed, wait := offer(v, t, busy, admit, now)
			out = append(out, claimed...)
			waits[t.ID] = wait
			// It enters only once someone takes it up there, so a stage
			// is never held for a person who is busy elsewhere.
			if next != "" && len(t.Claims) > 0 {
				stages.move(t, next)
			}
		}
		for _, p := range queueOrder(v) {
			out = append(out, startQueued(v, p, busy, admit, stages, waits, now)...)
		}
		for i := range v.Tasks {
			v.Tasks[i].Waiting = waits[v.Tasks[i].ID]
		}
		markBeside(v)
		return nil
	})
	return out, err
}

// queueOrder is the projects in the order their queued work was asked for:
// the project whose next task to start was asked for first goes first, so a
// person seated in several projects starts their tasks in the order they
// were asked for, not one project's ahead of another's.
func queueOrder(v *Snapshot) []*Project {
	next := func(p *Project) (time.Time, bool) {
		for _, t := range v.Tasks {
			if t.ProjectID == p.ID && t.Status == TaskQueued && !heldBack(v, t) {
				return t.CreatedAt, true
			}
		}
		return time.Time{}, false
	}
	var out []*Project
	for i := range v.Projects {
		out = append(out, &v.Projects[i])
	}
	slices.SortStableFunc(out, func(a, b *Project) int {
		at, aok := next(a)
		bt, bok := next(b)
		switch {
		case aok != bok:
			if aok {
				return -1
			}
			return 1
		case !aok:
			return 0
		}
		return at.Compare(bt)
	})
	return out
}

// markBeside records, on each task under way, the other tasks of its
// project under way with it, so a conflict between two tasks built side by
// side is told from one with work that landed before this was built.
func markBeside(v *Snapshot) {
	active := map[string][]*Task{}
	for i := range v.Tasks {
		if t := &v.Tasks[i]; t.Active() {
			active[t.ProjectID] = append(active[t.ProjectID], t)
		}
	}
	for _, tasks := range active {
		for _, t := range tasks {
			for _, o := range tasks {
				if o.ID != t.ID && !slices.Contains(t.Beside, o.ID) {
					t.Beside = append(t.Beside, o.ID)
				}
			}
		}
	}
}

// BuiltBeside reports whether two tasks were ever under way at the same time.
func (t Task) BuiltBeside(o Task) bool {
	return slices.Contains(t.Beside, o.ID) || slices.Contains(o.Beside, t.ID)
}

// personKey is who sits in seat r of team. A seat filled from a team member
// is that member, as the first, second, ... copy of the member's seats that
// hold the same roles: Claudius and Claudius #2, added as a copy of it, are
// two people, while a member's seats holding different roles, such as one
// that implements and one that reviews, are one person, and so is the
// member's first seat in every project. A template's seat, such as a new
// project's Implementer, is a person of its own in its project.
func personKey(projectID string, team []Role, r Role) string {
	if r.Member == "" {
		return "seat/" + projectID + "/" + strings.ToLower(strings.TrimSpace(r.Name))
	}
	copies := 0
	for _, o := range team {
		if strings.EqualFold(o.Name, r.Name) {
			break
		}
		if o.Member == r.Member && len(o.Kinds) == len(r.Kinds) && !slices.ContainsFunc(o.Kinds, func(k string) bool { return !r.Holds(k) }) {
			copies++
		}
	}
	return fmt.Sprintf("member/%s/%d", r.Member, copies)
}

// seatRole is the seat a claim on a task names, and the team it sits in: as
// the task pinned it, or else as the project's team has it now, as for the
// PM's seat held while a task is decided.
func seatRole(v *Snapshot, projectID string, pinned []Role, name string) (Role, []Role) {
	for _, roles := range [][]Role{pinned, playbookRoles(project(v, projectID))} {
		if i := slices.IndexFunc(roles, func(r Role) bool { return strings.EqualFold(r.Name, name) }); i >= 0 {
			return roles[i], roles
		}
	}
	return Role{Name: name}, nil
}

func playbookRoles(p *Project) []Role {
	if p == nil || p.Playbook == nil {
		return nil
	}
	return p.Playbook.Roles
}

// busyPeople are the people with a claim, a task's or a project's own, in
// any project, each with what they are busy on, as a Wait for them.
func busyPeople(v *Snapshot) map[string]Wait {
	busy := map[string]Wait{}
	for _, t := range v.Tasks {
		for _, c := range t.Claims {
			if c.Seat == "" {
				continue
			}
			r, team := seatRole(v, t.ProjectID, t.Roles, c.Seat)
			doing := onTask(v, t)
			doing.Kind, doing.Seat, doing.Member = WaitMember, r.Name, r.Member
			busy[personKey(t.ProjectID, team, r)] = doing
		}
	}
	for _, p := range v.Projects {
		for _, c := range p.Claims {
			if c.Seat != "" {
				r, team := seatRole(v, p.ID, nil, c.Seat)
				busy[personKey(p.ID, team, r)] = Wait{Kind: WaitMember, Seat: r.Name, Member: r.Member, List: p.Title}
			}
		}
	}
	return busy
}

// onTask is a person busy on t, as a Wait for them names it.
func onTask(v *Snapshot, t Task) Wait {
	if p := project(v, t.ProjectID); p != nil {
		return Wait{On: p.TaskRef(t.Number)}
	}
	return Wait{}
}

// freeSeat is the first of seats, on team, whose person is free and that
// admit lets run, which it marks busy on what doing names. With none, it
// says what the seats wait for: what admit refused, or else the first
// seat's person, busy elsewhere.
func freeSeat(projectID string, team, seats []Role, busy map[string]Wait, admit Admit, doing Wait) (Role, *Wait) {
	var wait *Wait
	for _, r := range seats {
		key := personKey(projectID, team, r)
		if on, ok := busy[key]; ok {
			if wait == nil {
				wait = &on
			}
			continue
		}
		if why := admit(r); why != "" {
			wait = &Wait{Kind: why}
			if why == WaitEngineCap {
				wait.Engine = r.Engine
			}
			continue
		}
		doing.Kind, doing.Seat, doing.Member = WaitMember, r.Name, r.Member
		busy[key] = doing
		return r, nil
	}
	return Role{}, wait
}

// stepKinds is the kind of role that takes each step a seat takes.
var stepKinds = map[string]string{TaskResearching: RoleResearcher, TaskDesigning: RoleDesigner, TaskWriting: RoleImplementer}

// offer claims what t can do next: its step, or each check its latest draft
// still needs. With nothing of the task under way or claimed, it says what
// a step that is ready waits for.
func offer(v *Snapshot, t *Task, busy map[string]Wait, admit Admit, now time.Time) ([]Scheduled, *Wait) {
	if t.alone() {
		return nil, nil
	}
	whole := func(step string, seat Role) ([]Scheduled, *Wait) {
		c := newClaim(t, Claim{Step: step, Seat: seat.Name}, now)
		return []Scheduled{{Task: *t, Claim: c, Seat: seat}}, nil
	}
	if kind, ok := stepKinds[t.Status]; ok {
		if len(t.Claims) > 0 {
			return nil, nil
		}
		seats := t.RolesOf(kind)
		// With no seat for it, the step itself says what happens instead.
		if len(seats) == 0 {
			return whole(t.Status, Role{})
		}
		seat, wait := freeSeat(t.ProjectID, t.Roles, seats, busy, admit, onTask(v, *t))
		if wait == nil {
			return whole(t.Status, seat)
		}
		return nil, wait
	}
	switch t.Status {
	case TaskReviewing:
		if len(t.Revisions) == 0 || t.DirectionPending > 0 {
			break
		}
		latest := t.Revisions[len(t.Revisions)-1].N
		brief := briefVersion(v, *t)
		var out []Scheduled
		var waiting *Wait
		pending := false
		for _, g := range t.CheckerGroups() {
			if t.Judged(g.Seats[0].Name, latest, brief) {
				continue
			}
			pending = true
			// A seat of the group already checking it, for this step or a
			// message, is the group's one check at a time.
			if slices.ContainsFunc(t.Claims, func(c Claim) bool { return c.Group == g.Key }) {
				continue
			}
			seat, wait := freeSeat(t.ProjectID, t.Roles, g.Seats, busy, admit, onTask(v, *t))
			if wait != nil {
				waiting = cmp.Or(waiting, wait)
				continue
			}
			c := newClaim(t, Claim{Step: TaskReviewing, Seat: seat.Name, Group: g.Key, Revision: latest, Shared: true}, now)
			out = append(out, Scheduled{Task: *t, Claim: c, Seat: seat})
		}
		if pending {
			if len(t.Claims) > 0 {
				return out, nil
			}
			return out, waiting
		}
	case TaskDeciding:
		p := project(v, t.ProjectID)
		if p != nil && PMGates(*p, *t) {
			if why := LandingHeld(p, *t); len(why) > 0 {
				return nil, &Wait{Kind: "blocker", On: strings.Join(why, "; ")}
			}
		}
	case TaskLanding:
		if why := landingStepHeld(project(v, t.ProjectID), *t); len(why) > 0 && t.Delivering == nil {
			return nil, &Wait{Kind: "blocker", On: strings.Join(why, "; ")}
		}
		// One landing at a time, counting one a stop cut off whose delivery
		// has yet to be settled.
		if slices.ContainsFunc(v.Tasks, func(o Task) bool {
			if o.ProjectID != t.ProjectID || o.ID == t.ID {
				return false
			}
			return (o.Finished() && o.Delivering != nil) || slices.ContainsFunc(o.Claims, func(c Claim) bool { return c.Step == TaskLanding })
		}) {
			return nil, nil
		}
	}
	if len(t.Claims) > 0 {
		return nil, nil
	}
	return whole(t.Status, Role{})
}

// startQueued starts a project's queued tasks in queue order, skipping
// those that wait for unfinished work, while the project is below its cap,
// its first stage has room and a seat is free for the first step. A queued
// task never starts ahead of one before it that could; the first that
// can't records in waits what it waits for.
func startQueued(v *Snapshot, p *Project, busy map[string]Wait, admit Admit, stages held, waits map[string]*Wait, now time.Time) []Scheduled {
	// Nothing starts while the PM is ordering the list: the order it sets
	// is the one tasks start in.
	if p.Paused || p.Playbook == nil || slices.ContainsFunc(p.Claims, func(c Claim) bool { return c.Step == StepPM }) {
		return nil
	}
	active := 0
	for _, t := range v.Tasks {
		if t.ProjectID == p.ID && t.Active() {
			active++
		}
	}
	var out []Scheduled
	for i := range v.Tasks {
		t := &v.Tasks[i]
		if t.ProjectID != p.ID || t.Status != TaskQueued || heldBack(v, *t) {
			continue
		}
		if limit := p.Playbook.ActiveCap(); limit > 0 && active >= limit {
			waits[t.ID] = &Wait{Kind: WaitProjectCap, Active: active, Cap: limit}
			break
		}
		status, stage := TaskWriting, StageImplementing
		if slices.ContainsFunc(p.Playbook.Roles, func(r Role) bool { return r.Holds(RoleResearcher) }) && t.Plan == nil {
			status, stage = TaskResearching, StageResearching
		}
		if wait := stages.room(v, p.ID, "", stage); wait != nil {
			waits[t.ID] = wait
			break
		}
		var seat Role
		if seats := rolesOf(p.Playbook.Roles, stepKinds[status]); len(seats) > 0 {
			var wait *Wait
			if seat, wait = freeSeat(p.ID, p.Playbook.Roles, seats, busy, admit, onTask(v, *t)); wait != nil {
				waits[t.ID] = wait
				break
			}
		}
		startTask(v, p, t, now)
		stages.move(t, stage)
		active++
		// The seat as the task pinned it, with its member's learnings.
		if pinned, ok := t.Role(seat.Name); ok {
			seat = pinned
		}
		c := newClaim(t, Claim{Step: t.Status, Seat: seat.Name}, now)
		out = append(out, Scheduled{Task: *t, Claim: c, Seat: seat})
	}
	return out
}

func rolesOf(roles []Role, kind string) []Role {
	var out []Role
	for _, r := range roles {
		if r.Holds(kind) {
			out = append(out, r)
		}
	}
	return out
}

// ClaimTask holds a task alone for a step no seat takes, such as the owner
// handing it a draft, and refuses while anything else holds it.
func (s *Service) ClaimTask(ctx context.Context, taskID, step string) (Claim, error) {
	var out Claim
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		if len(t.Claims) > 0 {
			return fmt.Errorf("the team is at work on this task right now: %w", ErrConflict)
		}
		out = newClaim(t, Claim{Step: step}, s.now().UTC())
		return nil
	})
	return out, err
}

// ClaimMessage takes the seat a message to a reviewer or QA is for, to
// answer it with a check of the latest draft, beside the task's other
// checks. It says whether it could: not while the seat's person is busy in
// any project, the task is held for another step, or the message is no
// longer waiting.
func (s *Service) ClaimMessage(ctx context.Context, taskID, messageID string, admit Admit) (Scheduled, bool, error) {
	var out Scheduled
	found := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		if v.ProjectPaused(t.ProjectID) {
			return nil
		}
		i := slices.IndexFunc(t.Messages, func(m TeamMessage) bool { return m.ID == messageID })
		if i < 0 || !t.Messages[i].Open() || t.alone() || t.Finished() || len(t.Revisions) == 0 {
			return nil
		}
		if slices.ContainsFunc(t.Claims, func(c Claim) bool { return c.Message == messageID }) {
			return nil
		}
		seat, ok := t.Role(t.Messages[i].To)
		// One check per checker group at a time: a seat of the same member
		// already checking the task, for its own step or another message,
		// is waited for, so the group never judges twice at once.
		group := t.CheckerGroup(t.Messages[i].To)
		if ok && slices.ContainsFunc(t.Claims, func(c Claim) bool { return c.Group == group }) {
			return nil
		}
		if ok {
			if _, wait := freeSeat(t.ProjectID, t.Roles, []Role{seat}, busyPeople(v), admit, Wait{}); wait != nil {
				return nil
			}
		}
		if t.Messages[i].Status == MessageWaiting {
			t.Messages[i].Status = MessageWorking
		}
		c := newClaim(t, Claim{Step: StepMessage, Seat: seat.Name, Group: group, Message: messageID, Revision: t.Revisions[len(t.Revisions)-1].N, Shared: true}, s.now().UTC())
		out, found = Scheduled{Task: *t, Claim: c, Seat: seat}, true
		return nil
	})
	return out, found, err
}

// ClaimPM takes the project's PM seat for its look at the to-do list, when
// the list is due a look, and says whether it could: not while the seat is
// at work on anything else, the project already has a step of its own
// under way, or admit refuses.
func (s *Service) ClaimPM(ctx context.Context, projectID string, admit Admit) (Claim, Role, bool, error) {
	return s.claimPM(ctx, projectID, StepPM, admit)
}

// ClaimPMQuestion takes the project's PM seat to answer the assistant's
// question, and says whether it could: as ClaimPM, but whether or not the
// list is due a look.
func (s *Service) ClaimPMQuestion(ctx context.Context, projectID string, admit Admit) (Claim, Role, bool, error) {
	return s.claimPM(ctx, projectID, StepPMQuestion, admit)
}

func (s *Service) claimPM(ctx context.Context, projectID, step string, admit Admit) (Claim, Role, bool, error) {
	var out Claim
	var seat Role
	found := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		var err error
		out, seat, found, err = s.takePM(v, projectID, step, admit)
		return err
	})
	return out, seat, found, err
}

// ReleaseProjectClaim clears a project's claim once its turn has ended.
func (s *Service) ReleaseProjectClaim(ctx context.Context, projectID, token string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if p := project(v, projectID); p != nil {
			s.stopPMChat(v, projectID, token)
			p.Claims = slices.DeleteFunc(p.Claims, func(c Claim) bool { return c.Token == token })
		}
		return nil
	})
}

// HoldSeat gives a step no seat took the seat it now needs, such as the
// PM's while a task is decided, for the rest of the step. It says whether
// it could: not while the seat's person is at work on anything else, in any
// project.
func (s *Service) HoldSeat(ctx context.Context, taskID, token, seat string) (bool, error) {
	held := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
		}
		c := t.claim(token)
		if c == nil {
			return fmt.Errorf("%s: %w", token, ErrStale)
		}
		if strings.EqualFold(c.Seat, seat) {
			held = true
			return nil
		}
		r, team := seatRole(v, t.ProjectID, t.Roles, seat)
		if _, busy := busyPeople(v)[personKey(t.ProjectID, team, r)]; c.Seat != "" || busy {
			return nil
		}
		c.Seat, held = seat, true
		return nil
	})
	return held, err
}

// ReleaseClaim clears a claim once its turn has ended, freeing its seat. A
// claim already gone, because the task was stopped, is left gone.
func (s *Service) ReleaseClaim(ctx context.Context, taskID, token string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return nil
		}
		t.Claims = slices.DeleteFunc(t.Claims, func(c Claim) bool { return c.Token == token })
		return nil
	})
}

// RecoverClaims clears the claims a stopped daemon left, in one change,
// and moves each such task on to an attempt of its own, so every old token
// is dead. A restart is no one's failure, so none is counted. held names
// the claims whose turns could not be confirmed ended, with why: they stay,
// holding their task and seat, rather than have their step run twice.
func (s *Service) RecoverClaims(ctx context.Context, held map[string]string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		for i := range v.Tasks {
			t := &v.Tasks[i]
			// Who a step waited for may have been busy only on a turn that
			// has gone; the next look says again.
			t.Waiting = nil
			if len(t.Claims) == 0 {
				continue
			}
			t.Attempt++
			var kept []Claim
			for _, c := range t.Claims {
				if why, ok := held[c.Token]; ok {
					c.Held = why
					kept = append(kept, c)
				}
			}
			t.Claims = kept
			if len(kept) > 0 {
				t.Detail = "Held: a turn from before the restart may still be running"
			}
		}
		// A project's own steps, such as the PM's look, likewise.
		for i := range v.Projects {
			p := &v.Projects[i]
			if len(p.Claims) == 0 {
				continue
			}
			p.Attempt++
			p.Claims = slices.DeleteFunc(p.Claims, func(c Claim) bool {
				_, ok := held[c.Token]
				if !ok {
					s.stopPMChat(v, p.ID, c.Token)
				}
				return !ok
			})
			for k := range p.Claims {
				p.Claims[k].Held = held[p.Claims[k].Token]
			}
		}
		return nil
	})
}

// ActiveCap is the optional overall limit; 0 means no overall limit.
func (p Playbook) ActiveCap() int { return p.MaxActive }

// CheckerGroup is the seats that judge a draft as one: the seats filled from
// one team member, who are interchangeable, or one seat of no member. A
// draft needs one verdict from each group.
type CheckerGroup struct {
	Key   string
	Seats []Role
}

// groupKey is the checker group a seat judges in.
func groupKey(r Role) string {
	if r.Member != "" {
		return r.Working() + "/member/" + r.Member
	}
	return r.Working() + "/seat/" + strings.ToLower(strings.TrimSpace(r.Name))
}

// CheckerGroup is the group the named seat judges in; a seat no longer on
// the team is a group of its own.
func (t Task) CheckerGroup(seat string) string {
	if r, ok := t.Role(seat); ok {
		return groupKey(r)
	}
	return "/seat/" + strings.ToLower(strings.TrimSpace(seat))
}

// CheckerGroups are the task's checker groups, reviewers first, then QA, in
// team order.
func (t Task) CheckerGroups() []CheckerGroup {
	var out []CheckerGroup
	for _, r := range append(t.RolesOf(RoleReviewer), t.RolesOf(RoleQA)...) {
		key := groupKey(r)
		if i := slices.IndexFunc(out, func(g CheckerGroup) bool { return g.Key == key }); i >= 0 {
			out[i].Seats = append(out[i].Seats, r)
			continue
		}
		out = append(out, CheckerGroup{Key: key, Seats: []Role{r}})
	}
	return out
}

func (s *Service) takePM(v *Snapshot, projectID, step string, admit Admit) (Claim, Role, bool, error) {
	p := project(v, projectID)
	if p == nil {
		return Claim{}, Role{}, false, ErrNotFound
	}
	pm, ok := p.PMSeat()
	if !ok || (step == StepPM && (p.Paused || !p.PMDue)) || len(p.Claims) > 0 {
		return Claim{}, Role{}, false, nil
	}
	var wait *Wait
	var seat Role
	if seat, wait = freeSeat(p.ID, p.Playbook.Roles, []Role{pm}, busyPeople(v), admit, Wait{}); wait != nil {
		return Claim{}, Role{}, false, nil
	}
	p.Attempt++
	out := Claim{Token: fmt.Sprintf("%s/%s/%d", p.ID, step, p.Attempt), Step: step, Seat: seat.Name, At: s.now().UTC()}
	p.Claims = append(p.Claims, out)
	return out, seat, true, nil
}
