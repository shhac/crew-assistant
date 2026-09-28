package core

import (
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
	// Message is the team message a StepMessage answers.
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
// a slot on its engine; false leaves the step for a later look.
type Admit func(Role) bool

// Schedule claims every step that can start now, in one change: the next
// steps of started tasks first, furthest along first, then queued tasks in
// queue order while their project is below its cap on active tasks. A seat
// takes one step at a time. A task's checks each take a seat of their own,
// side by side; any other step holds the task alone, and a project lands one
// task at a time.
func (s *Service) Schedule(ctx context.Context, admit Admit) ([]Scheduled, error) {
	var out []Scheduled
	err := s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		busy := busySeats(v)
		var active []*Task
		for i := range v.Tasks {
			if v.Tasks[i].Active() {
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
			if t.RetryAt.After(now) || t.Handoff != nil {
				continue
			}
			out = append(out, offer(v, t, busy, admit, now)...)
		}
		for i := range v.Projects {
			out = append(out, startQueued(v, &v.Projects[i], busy, admit, now)...)
		}
		markBeside(v)
		return nil
	})
	return out, err
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

// busySeats are the seats with a claim, a task's or the project's own, by
// project and seat name.
func busySeats(v *Snapshot) map[string]bool {
	busy := map[string]bool{}
	mark := func(projectID string, claims []Claim) {
		for _, c := range claims {
			if c.Seat != "" {
				busy[seatKey(projectID, c.Seat)] = true
			}
		}
	}
	for _, t := range v.Tasks {
		mark(t.ProjectID, t.Claims)
	}
	for _, p := range v.Projects {
		mark(p.ID, p.Claims)
	}
	return busy
}

func seatKey(projectID, seat string) string {
	return projectID + "/" + strings.ToLower(strings.TrimSpace(seat))
}

// freeSeat is the first of seats with no claim that admit lets run, which it
// marks busy.
func freeSeat(projectID string, seats []Role, busy map[string]bool, admit Admit) (Role, bool) {
	for _, r := range seats {
		key := seatKey(projectID, r.Name)
		if busy[key] || !admit(r) {
			continue
		}
		busy[key] = true
		return r, true
	}
	return Role{}, false
}

// stepKinds is the kind of role that takes each step a seat takes.
var stepKinds = map[string]string{TaskResearching: RoleResearcher, TaskDesigning: RoleDesigner, TaskWriting: RoleImplementer}

// offer claims what t can do next: its step, or each check its latest draft
// still needs.
func offer(v *Snapshot, t *Task, busy map[string]bool, admit Admit, now time.Time) []Scheduled {
	if t.alone() {
		return nil
	}
	whole := func(step string, seat Role) []Scheduled {
		c := newClaim(t, Claim{Step: step, Seat: seat.Name}, now)
		return []Scheduled{{Task: *t, Claim: c, Seat: seat}}
	}
	if kind, ok := stepKinds[t.Status]; ok {
		if len(t.Claims) > 0 {
			return nil
		}
		seats := t.RolesOf(kind)
		// With no seat for it, the step itself says what happens instead.
		if len(seats) == 0 {
			return whole(t.Status, Role{})
		}
		if seat, ok := freeSeat(t.ProjectID, seats, busy, admit); ok {
			return whole(t.Status, seat)
		}
		return nil
	}
	switch t.Status {
	case TaskReviewing:
		if len(t.Revisions) == 0 || t.DirectionPending > 0 {
			break
		}
		latest := t.Revisions[len(t.Revisions)-1].N
		brief := briefVersion(v, *t)
		var out []Scheduled
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
			seat, ok := freeSeat(t.ProjectID, g.Seats, busy, admit)
			if !ok {
				continue
			}
			c := newClaim(t, Claim{Step: TaskReviewing, Seat: seat.Name, Group: g.Key, Revision: latest, Shared: true}, now)
			out = append(out, Scheduled{Task: *t, Claim: c, Seat: seat})
		}
		if pending {
			return out
		}
	case TaskLanding:
		if slices.ContainsFunc(v.Tasks, func(o Task) bool {
			return o.ProjectID == t.ProjectID && o.ID != t.ID && slices.ContainsFunc(o.Claims, func(c Claim) bool { return c.Step == TaskLanding })
		}) {
			return nil
		}
	}
	if len(t.Claims) > 0 {
		return nil
	}
	return whole(t.Status, Role{})
}

// startQueued starts a project's queued tasks in queue order, skipping
// those that wait for unfinished work, while the project is below its cap
// and a seat is free for the first step. A queued task never starts ahead
// of one before it that could.
func startQueued(v *Snapshot, p *Project, busy map[string]bool, admit Admit, now time.Time) []Scheduled {
	// Nothing starts while the PM is ordering the list: the order it sets
	// is the one tasks start in.
	if p.Playbook == nil || slices.ContainsFunc(p.Claims, func(c Claim) bool { return c.Step == StepPM }) {
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
		if t.ProjectID != p.ID || t.Status != TaskQueued || len(waitsFor(v, *t)) > 0 {
			continue
		}
		if active >= p.Playbook.ActiveCap() {
			break
		}
		status := TaskWriting
		if slices.ContainsFunc(p.Playbook.Roles, func(r Role) bool { return r.Holds(RoleResearcher) }) && t.Plan == nil {
			status = TaskResearching
		}
		var seat Role
		if seats := rolesOf(p.Playbook.Roles, stepKinds[status]); len(seats) > 0 {
			var ok bool
			if seat, ok = freeSeat(p.ID, seats, busy, admit); !ok {
				break
			}
		}
		startTask(v, p, t, now)
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
// checks. It says whether it could: not while the seat is busy, the task is
// held for another step, or the message is no longer waiting.
func (s *Service) ClaimMessage(ctx context.Context, taskID, messageID string, admit Admit) (Scheduled, bool, error) {
	var out Scheduled
	found := false
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil {
			return ErrNotFound
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
		busy := busySeats(v)
		if ok {
			if seat, ok = freeSeat(t.ProjectID, []Role{seat}, busy, admit); !ok {
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
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		pm, ok := p.PMSeat()
		if !ok || (step == StepPM && !p.PMDue) || len(p.Claims) > 0 {
			return nil
		}
		if seat, ok = freeSeat(p.ID, []Role{pm}, busySeats(v), admit); !ok {
			return nil
		}
		p.Attempt++
		out = Claim{Token: fmt.Sprintf("%s/%s/%d", p.ID, step, p.Attempt), Step: step, Seat: seat.Name, At: s.now().UTC()}
		p.Claims = append(p.Claims, out)
		found = true
		return nil
	})
	return out, seat, found, err
}

// ReleaseProjectClaim clears a project's claim once its turn has ended.
func (s *Service) ReleaseProjectClaim(ctx context.Context, projectID, token string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if p := project(v, projectID); p != nil {
			p.Claims = slices.DeleteFunc(p.Claims, func(c Claim) bool { return c.Token == token })
		}
		return nil
	})
}

// HoldSeat gives a step no seat took the seat it now needs, such as the
// PM's while a task is decided, for the rest of the step. It says whether
// it could: not while the seat is at work on anything else.
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
		if c.Seat != "" || busySeats(v)[seatKey(t.ProjectID, seat)] {
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
				return !ok
			})
			for k := range p.Claims {
				p.Claims[k].Held = held[p.Claims[k].Token]
			}
		}
		return nil
	})
}

// ActiveCap is how many of a project's tasks may be under way at once: the
// owner's setting, or else one for each implementer seat.
func (p Playbook) ActiveCap() int {
	if p.MaxActive > 0 {
		return p.MaxActive
	}
	return max(1, len(rolesOf(p.Roles, RoleImplementer)))
}

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
