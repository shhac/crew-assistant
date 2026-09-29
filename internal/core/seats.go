package core

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Unseat takes a kind of role from every seat that holds it; a seat goes if
// that was all it held. It returns where a seat for the kind belongs: where
// the first of them was. The roles are copied first, so a team a task
// started with never changes.
func (p *Playbook) Unseat(kind string) int {
	p.Roles = slices.Clone(p.Roles)
	at := -1
	for k := 0; k < len(p.Roles); {
		switch {
		case !p.Roles[k].Holds(kind):
			k++
			continue
		case len(p.Roles[k].Kinds) == 1:
			p.Roles = slices.Delete(p.Roles, k, k+1)
			if at < 0 {
				at = k
			}
			continue
		}
		p.Rekind(k, slices.DeleteFunc(slices.Clone(p.Roles[k].Kinds), func(k string) bool { return k == kind }))
		if at < 0 {
			at = k + 1
		}
		k++
	}
	if at < 0 {
		return len(p.Roles)
	}
	return at
}

// AddSeat adds another seat filled like the named one, after the last seat
// filled like it: Claudius gains Claudius #2, then Claudius #3. Each seat
// takes one step at a time, so more seats let more work run at once.
func (p *Playbook) AddSeat(name string) (Role, error) {
	k := slices.IndexFunc(p.Roles, func(r Role) bool { return strings.EqualFold(strings.TrimSpace(r.Name), strings.TrimSpace(name)) })
	if k < 0 {
		return Role{}, fmt.Errorf("the team has no seat named %q: %w", name, ErrNotFound)
	}
	p.Roles = slices.Clone(p.Roles)
	seat := p.Roles[k]
	seat.Kinds = slices.Clone(seat.Kinds)
	seat.Learnings = nil
	seat.Name = p.FreeName(-1, SeatBase(seat.Name))
	last := k
	for i, r := range p.Roles {
		if sameSeat(r, p.Roles[k]) {
			last = i
		}
	}
	p.Roles = slices.Insert(p.Roles, last+1, seat)
	return seat, nil
}

// RemoveSeat takes the named seat off the team. Tasks under way keep the
// team they started with.
func (p *Playbook) RemoveSeat(name string) error {
	k := slices.IndexFunc(p.Roles, func(r Role) bool { return strings.EqualFold(strings.TrimSpace(r.Name), strings.TrimSpace(name)) })
	if k < 0 {
		return fmt.Errorf("the team has no seat named %q: %w", name, ErrNotFound)
	}
	p.Roles = slices.Delete(slices.Clone(p.Roles), k, k+1)
	return nil
}

// sameSeat reports seats filled alike: from the same member, or the same
// template seat, holding the same kinds of role.
func sameSeat(a, b Role) bool {
	return a.Member == b.Member && SeatBase(a.Name) == SeatBase(b.Name) && slices.Equal(a.Kinds, b.Kinds)
}

var seatNumber = regexp.MustCompile(` #\d+$`)

// SeatBase is a seat's name without the number another seat filled like it
// carries: Claudius for Claudius #2.
func SeatBase(name string) string {
	return seatNumber.ReplaceAllString(strings.TrimSpace(name), "")
}

// TemplateInstructions is what the template says about how each of these
// kinds of role is done here, in the order a team works, whatever order the
// kinds were given in.
func (p Playbook) TemplateInstructions(kinds []string) string {
	var parts []string
	for _, kind := range roleKinds {
		t := slices.IndexFunc(Templates[p.Template].Roles, func(r Role) bool { return r.Holds(kind) })
		if slices.Contains(kinds, kind) && t >= 0 {
			parts = append(parts, Templates[p.Template].Roles[t].Instructions)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Rekind changes the kinds of role the k-th seat holds. Its instructions
// become the template's for those kinds, followed by the seat's own, so a
// seat holds the same instructions however its roles were given to it.
func (p *Playbook) Rekind(k int, kinds []string) {
	r := &p.Roles[k]
	own := p.ownInstructions(*r)
	r.Kinds = kinds
	r.Instructions = strings.TrimSpace(p.TemplateInstructions(kinds) + "\n\n" + own)
	// The browser is QA's, so it goes with the role.
	if !r.Holds(RoleQA) {
		r.Browser = Browser{}
	}
}

// ownInstructions is what a seat was told beyond the template's instructions
// for its roles. A seat that took a second role before seats were told about
// each carries the template's instructions for one role only, so that is
// looked for too.
func (p Playbook) ownInstructions(r Role) string {
	prefixes := []string{p.TemplateInstructions(r.Kinds)}
	for _, kind := range r.Kinds {
		prefixes = append(prefixes, p.TemplateInstructions([]string{kind}))
	}
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(r.Instructions, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(r.Instructions, prefix))
		}
	}
	return strings.TrimSpace(r.Instructions)
}

// NameSeats gives way to members: a template seat named like a member's
// seat takes a number, so every seat keeps a name of its own.
func (p *Playbook) NameSeats() {
	for i, r := range p.Roles {
		if r.Member == "" && slices.ContainsFunc(p.Roles, func(o Role) bool {
			return o.Member != "" && strings.EqualFold(strings.TrimSpace(o.Name), strings.TrimSpace(r.Name))
		}) {
			p.Roles[i].Name = p.FreeName(i, r.Name)
		}
	}
}

// TemplateSeat is the template's seat for a kind of role, to fill it when no
// member does, named so that no seat on the team shares its name.
func (p Playbook) TemplateSeat(kind string) (Role, bool) {
	template := Templates[p.Template].Roles
	t := slices.IndexFunc(template, func(r Role) bool { return r.Holds(kind) })
	if t < 0 {
		return Role{}, false
	}
	seat := template[t]
	seat.Kinds = []string{kind}
	seat.Name = p.FreeName(-1, seat.Name)
	return seat, true
}

// FreeName is name, or name numbered as Claudius #2, whichever no seat but
// the k-th has. Seat names must differ by more than case.
func (p Playbook) FreeName(k int, name string) string {
	taken := func(candidate string) bool {
		for i, r := range p.Roles {
			if i != k && strings.EqualFold(strings.TrimSpace(r.Name), candidate) {
				return true
			}
		}
		return false
	}
	candidate := name
	for n := 2; taken(candidate); n++ {
		candidate = fmt.Sprintf("%s #%d", name, n)
	}
	return candidate
}
