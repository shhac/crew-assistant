package core

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// HandOn asks another person to take the next turn of this kind or group.
type HandOn struct {
	// Kind is the role whose next turn is handed on.
	Kind string `json:"kind"`
	// Group identifies a checker group; empty for other roles.
	Group string `json:"group,omitempty"`
	// Seat names the requester.
	Seat string `json:"seat"`
	// Member identifies the requester across seats.
	Member string `json:"member,omitempty"`
	// Why records the reason for another person to take over.
	Why string `json:"why"`
	// At is the service time when the request was recorded.
	At time.Time `json:"at"`
}

// NextTaker describes the same ordering that Schedule uses, before a claim.
type NextTaker struct {
	// Kind is the role taking the step.
	Kind string `json:"kind"`
	// Group identifies the checker group, if any.
	Group string `json:"group,omitempty"`
	// Preferred names the prior worker or retry claimant still on the task.
	Preferred string `json:"preferred,omitempty"`
	// Did describes the recorded earlier work.
	Did string `json:"did,omitempty"`
	// Others lists fallback seats, or all candidates without a preference.
	Others []string `json:"others,omitempty"`
}

// TurnEnd is the optional reply metadata recorded with a turn's outcome.
// It is a call argument, never part of a persisted plan or verdict.
type TurnEnd struct {
	HandOnWhy string
	Problems  []string
}

// recordTurnEnd runs inside updateTask, which logs new hand-ons after its callback.
// Other store updates use recordTurnEndLogged instead.
func recordTurnEnd(v *Snapshot, t *Task, kind, group string, seat Role, end TurnEnd, now time.Time) {
	t.AskHandOn(kind, group, seat, end.HandOnWhy, now)
	for _, problem := range end.Problems {
		recordTask(v, now, t, "task.reply_problem", seat.Name+": "+problem)
	}
}

func (h HandOn) description() string {
	return fmt.Sprintf("%s asked for someone else to take the next %s: %s", h.Seat, handOnWork(h.Kind), h.Why)
}

func handOnWork(kind string) string {
	switch kind {
	case RoleImplementer:
		return "build"
	case RoleResearcher:
		return "research"
	}
	return "check"
}

// AskHandOn is called inside the update that records the turn's outcome.
func (t *Task) AskHandOn(kind, group string, seat Role, why string, at time.Time) {
	if strings.TrimSpace(why) == "" {
		return
	}
	t.HandOn = slices.DeleteFunc(t.HandOn, func(h HandOn) bool { return h.Kind == kind && h.Group == group })
	t.HandOn = append(t.HandOn, HandOn{Kind: kind, Group: group, Seat: seat.Name, Member: seat.Member, Why: why, At: at})
}

func logHandOn(v *Snapshot, t *Task, before []HandOn) {
	for _, h := range t.HandOn {
		if !slices.Contains(before, h) {
			recordTask(v, h.At, t, "task.hand_on", h.description())
		}
	}
}

func useHandOn(v *Snapshot, t *Task, kind, group string, seat Role, now time.Time) {
	t.HandOn = slices.DeleteFunc(t.HandOn, func(h HandOn) bool {
		if h.Kind != kind || h.Group != group {
			return false
		}
		if h.Seat == seat.Name {
			recordTask(v, now, t, "task.hand_on_used", seat.Name+" took the next "+handOnWork(kind)+": no one else was free")
		}
		return true
	})
}

func (t Task) preferredSeats(kind, group string) ([]Role, NextTaker) {
	seats := t.RolesOf(kind)
	if group != "" {
		seats = slices.DeleteFunc(seats, func(r Role) bool { return groupKey(r) != group })
	}
	next := NextTaker{Kind: kind, Group: group}
	preferred, did := "", ""
	switch kind {
	case RoleDesigner:
		if request := t.OpenDesign(); request != nil {
			for _, seat := range seats {
				if seat.Name == request.For {
					return []Role{seat}, NextTaker{Kind: kind, Preferred: seat.Name}
				}
			}
		}
	case RoleImplementer:
		if r, ok := t.Writer(); ok {
			preferred = r.Name
		}
		for i := len(t.Revisions) - 1; i >= 0; i-- {
			r := t.Revisions[i]
			if r.Seat == preferred && preferred != "" {
				did = fmt.Sprintf("build %d", r.N)
				break
			}
		}
	case RoleResearcher:
		if t.Plan != nil {
			preferred, did = t.Plan.Role, "research"
		}
		for i := len(t.Research) - 1; i >= 0; i-- {
			pass := t.Research[i]
			if pass.Researcher != "" && (t.Plan == nil || pass.AnsweredAt.After(t.Plan.At)) {
				preferred, did = pass.Researcher, "research"
				break
			}
		}
	case RoleReviewer, RoleQA:
		latest := 0
		if len(t.Revisions) > 0 {
			latest = t.Revisions[len(t.Revisions)-1].N
		}
		latest = max(latest, t.Round)
		best := -1
		for i := len(t.Verdicts) - 1; i >= 0; i-- {
			v := t.Verdicts[i]
			if !v.Outside && v.Revision <= latest && v.Revision > best && t.CheckerGroup(v.Role) == group {
				preferred, did, best = v.Role, fmt.Sprintf("checked draft %d", v.Revision), v.Revision
			}
		}
	}
	if preferred == "" && !t.workedBefore(kind, group) {
		preferred = t.FirstSeats[seatKey(kind, group)]
	}
	if i := slices.IndexFunc(seats, func(r Role) bool { return r.Name == preferred }); i >= 0 {
		r := seats[i]
		seats = append([]Role{r}, append(seats[:i:i], seats[i+1:]...)...)
		next.Preferred, next.Did = preferred, did
	}
	for _, h := range t.HandOn {
		if h.Kind != kind || h.Group != group {
			continue
		}
		if i := slices.IndexFunc(seats, func(r Role) bool { return r.Name == h.Seat }); i >= 0 {
			key := personKey(t.ProjectID, t.Roles, seats[i])
			var others, handed []Role
			for _, r := range seats {
				if personKey(t.ProjectID, t.Roles, r) == key {
					handed = append(handed, r)
				} else {
					others = append(others, r)
				}
			}
			seats = append(others, handed...)
			if slices.ContainsFunc(handed, func(r Role) bool { return r.Name == next.Preferred }) {
				next.Preferred, next.Did = "", ""
			}
		}
	}
	for _, r := range seats {
		if r.Name != next.Preferred {
			next.Others = append(next.Others, r.Name)
		}
	}
	return seats, next
}

func (t Task) nextTakers(brief int) []NextTaker {
	if (!t.Active() && t.Status != TaskQueued && t.Status != TaskTriage) || t.Status == TaskLanding || t.Status == TaskAwaiting || t.Status == TaskDeciding {
		return nil
	}
	var out []NextTaker
	for _, kind := range []string{RoleResearcher, RoleDesigner, RoleImplementer} {
		if t.Status == TaskReviewing {
			continue
		}
		if kind == RoleResearcher && t.Plan != nil && t.Status != TaskResearching && !(t.Status == TaskDesigning && t.OpenResearch() != nil) {
			continue
		}
		claimed := slices.ContainsFunc(t.Claims, func(c Claim) bool { return stepKinds[c.Step] == kind })
		if seats, next := t.preferredSeats(kind, ""); len(seats) > 0 && !claimed {
			out = append(out, next)
		}
	}
	for _, g := range t.CheckerGroups() {
		if t.AcceptanceStands(brief) && (!g.Seats[0].Holds(RoleQA) || t.MergeCheckPending(brief)) {
			continue
		}
		if t.Status == TaskReviewing && len(t.Revisions) > 0 && t.Judged(g.Seats[0].Name, t.Revisions[len(t.Revisions)-1].N, brief) {
			continue
		}
		if !slices.ContainsFunc(t.Claims, func(c Claim) bool { return c.Group == g.Key }) {
			_, next := t.preferredSeats(g.Seats[0].Working(), g.Key)
			out = append(out, next)
		}
	}
	return out
}

// firstRoundSeat spreads first turns across the free seats. Later turns,
// including a hand-on or a removed preferred seat, retain preference order.
// The cursor is durable and moves in the same transaction as the claim.
func firstRoundSeat(v *Snapshot, t Task, kind, group string, seats []Role, busy map[string]busyWork, admit Admit) (Role, *Wait) {
	p := project(v, t.ProjectID)
	rotate := p != nil && len(seats) > 1 && !t.workedBefore(kind, group) && !slices.ContainsFunc(seats, func(r Role) bool { return r.Name == t.FirstSeats[seatKey(kind, group)] })
	key := seatKey(kind, group)
	if rotate {
		if i := slices.IndexFunc(seats, func(r Role) bool { return r.Name == p.SeatRotation[key] }); i >= 0 {
			seats = append(slices.Clone(seats[i+1:]), seats[:i+1]...)
		}
	}
	seat, wait := freeSeat(v, t.ProjectID, t.Roles, seats, busy, admit, onTask(v, t))
	if rotate && wait == nil {
		if p.SeatRotation == nil {
			p.SeatRotation = map[string]string{}
		}
		p.SeatRotation[key] = seat.Name
	}
	return seat, wait
}

func (t Task) workedBefore(kind, group string) bool {
	if slices.ContainsFunc(t.HandOn, func(h HandOn) bool { return h.Kind == kind && h.Group == group }) {
		return true
	}
	switch kind {
	case RoleImplementer:
		return slices.ContainsFunc(t.Revisions, func(r Revision) bool { return r.By != DraftByOwner && r.CleanMergeOf == 0 }) ||
			slices.ContainsFunc(t.Threads, func(th Thread) bool { return th.Kind == kind })
	case RoleResearcher:
		return t.Plan != nil || slices.ContainsFunc(t.Research, func(r ResearchRequest) bool { return r.Researcher != "" })
	case RoleReviewer, RoleQA:
		return slices.ContainsFunc(t.Verdicts, func(verdict Verdict) bool { return !verdict.Outside && t.CheckerGroup(verdict.Role) == group })
	default:
		// Designer selection is outside the first-round rotation requirement.
		return true
	}
}

func seatKey(kind, group string) string {
	if group != "" {
		return kind + "/" + group
	}
	return kind
}

// recordTurnEndLogged records and logs in store updates outside updateTask.
func recordTurnEndLogged(v *Snapshot, t *Task, kind, group string, seat Role, end TurnEnd, now time.Time) {
	before := slices.Clone(t.HandOn)
	recordTurnEnd(v, t, kind, group, seat, end, now)
	logHandOn(v, t, before)
}
