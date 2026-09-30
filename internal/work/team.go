package work

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

// TeamChoice is a team as the owner or the assistant chooses it. max_rounds
// is text, as a form or a tool sends it.
type TeamChoice struct {
	Template       string `json:"template"`
	WriterEngine   string `json:"writer_engine"`
	ReviewerEngine string `json:"reviewer_engine"`
	MaxRounds      string `json:"max_rounds"`
	DeliverTo      string `json:"deliver_to"`
	// Code teams only.
	Repo         string   `json:"repo"`
	BranchPrefix string   `json:"branch_prefix"`
	Check        string   `json:"check"`
	Prepare      []string `json:"prepare"`
	Sign         string   `json:"sign"`
	// CheckInCopy is "yes" for QA to run the check in a writable copy, "no"
	// for the read-only checkout, and empty to keep what the team has.
	CheckInCopy string `json:"check_in_copy"`
	// CheckLoopback is "yes" to let QA's check use this machine's own
	// addresses, "no" to refuse it, and empty to keep what the team has.
	CheckLoopback string `json:"check_loopback"`
	// Run, when given, is how QA starts the app to use it; nil keeps the
	// recipe the team has.
	Run *core.RunRecipe `json:"run,omitempty"`
	// Members to fill a role with, by id; empty keeps the template's role.
	Implementer string `json:"implementer_member"`
	Reviewer    string `json:"reviewer_member"`
	QA          string `json:"qa_member"`
	// Researcher is the member who researches each task first, or
	// NoResearcher for a team that starts writing at once.
	Researcher string `json:"researcher_member"`
	// Designer is the member the researcher and the implementer can hand a
	// task to for design input; empty or NoResearcher leaves the team
	// without one.
	Designer string `json:"designer_member"`
	// PM is the member who keeps the to-do list in order; empty leaves the
	// order to the owner and the assistant.
	PM string `json:"pm_member"`
}

// NoResearcher, as the researcher, leaves research out of a team.
const NoResearcher = "none"

// teamFrom builds a playbook from a template and the few choices the assistant
// may make about it. Anything left empty keeps the template's choice. current
// is the team as it stands, if there is one.
func teamFrom(in TeamChoice, snap core.Snapshot, current *core.Playbook) (core.Playbook, error) {
	template := in.Template
	if template == "" {
		template = "draft"
	}
	playbook, ok := core.Templates[template]
	if !ok {
		return core.Playbook{}, fmt.Errorf("unknown team template %q", template)
	}
	playbook.Roles = append([]core.Role(nil), playbook.Roles...)
	for i := range playbook.Roles {
		switch {
		case playbook.Roles[i].Holds(core.RoleImplementer) && in.WriterEngine != "":
			playbook.Roles[i].Engine = in.WriterEngine
		case playbook.Roles[i].Holds(core.RoleReviewer) && in.ReviewerEngine != "":
			playbook.Roles[i].Engine = in.ReviewerEngine
		}
	}
	if in.Researcher == NoResearcher {
		playbook.Roles = slices.DeleteFunc(playbook.Roles, func(r core.Role) bool { return r.Holds(core.RoleResearcher) && r.Working() == "" })
	}
	// The researcher, the designer and the PM come last, so a member who also
	// fills another seat does that from the same seat.
	for _, slot := range [][2]string{{core.RoleImplementer, in.Implementer}, {core.RoleReviewer, in.Reviewer}, {core.RoleQA, in.QA}, {core.RoleResearcher, in.Researcher}, {core.RoleDesigner, in.Designer}, {core.RolePM, in.PM}} {
		if slot[1] == "" || slot[1] == NoResearcher {
			continue
		}
		if err := fillRole(&playbook, slot[0], slot[1], snap, current); err != nil {
			return core.Playbook{}, err
		}
	}
	if in.MaxRounds != "" {
		rounds, err := strconv.Atoi(in.MaxRounds)
		if err != nil {
			return core.Playbook{}, fmt.Errorf("max_rounds must be a number")
		}
		playbook.MaxRounds = rounds
	}
	playbook.DeliverTo = in.DeliverTo
	if playbook.Medium == core.MediumGit {
		playbook.Repo = in.Repo
		if in.BranchPrefix != "" {
			playbook.BranchPrefix = in.BranchPrefix
		}
		playbook.Check = in.Check
		playbook.Prepare = append([]string(nil), in.Prepare...)
		playbook.Sign = in.Sign
		playbook.CheckInCopy = in.CheckInCopy == "yes"
		playbook.CheckLoopback = in.CheckLoopback == "yes"
	}
	return playbook, nil
}

// fillRole puts a member in the template's seat for that kind of role. The
// template's instructions stay, since they say how this kind of work is done
// here; the member's own follow them. A member already in another seat takes
// the role in that seat instead, so it is one seat, working on one thing at a
// time, rather than the same member twice. A member who holds the role in
// the current team keeps it, even if it no longer holds that kind: the
// team's copy is what counts until the owner changes it.
func fillRole(playbook *core.Playbook, kind, id string, snap core.Snapshot, current *core.Playbook) error {
	m, err := memberFor(kind, id, snap)
	if err != nil && current != nil && slices.ContainsFunc(current.Roles, func(r core.Role) bool { return r.Member == id && r.Holds(kind) }) {
		m, _ = snap.Member(id)
		err = nil
	}
	if err != nil {
		return err
	}
	slot := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Holds(kind) })
	// No template has a PM or a designer, so only they may join a team
	// without a slot.
	if slot < 0 && !memberOnly(kind) {
		return fmt.Errorf("a %s team has no %s for %s to fill", playbook.Template, kind, m.Name)
	}
	if seat := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Member == m.ID }); seat >= 0 && seat != slot {
		joinSeat(playbook, seat, m, kind)
		if slot >= 0 {
			playbook.Roles = slices.Delete(playbook.Roles, slot, slot+1)
		}
		return nil
	}
	if slot < 0 {
		playbook.Roles = append(playbook.Roles, memberSeat(m, []string{kind}, ""))
		return nil
	}
	playbook.Roles[slot] = memberSeat(m, playbook.Roles[slot].Kinds, playbook.Roles[slot].Instructions)
	return nil
}

// memberOnly reports a kind of role no template seats, which a team has only
// when a member holds it.
func memberOnly(kind string) bool { return kind == core.RolePM || kind == core.RoleDesigner }

// memberFor is the member id names, if it holds that kind of role.
func memberFor(kind, id string, snap core.Snapshot) (core.Member, error) {
	m, ok := snap.Member(id)
	if !ok {
		return core.Member{}, fmt.Errorf("there is no team member %q: %w", id, core.ErrNotFound)
	}
	if !m.Holds(kind) {
		return core.Member{}, fmt.Errorf("%s doesn't hold the %s role; they hold %s", m.Name, kind, strings.Join(m.Kinds, ", "))
	}
	return m, nil
}

// memberSeat is a member in a seat holding kinds of role, after the
// template's instructions for them, and how the member writes.
func memberSeat(m core.Member, kinds []string, instructions string) core.Role {
	instructions = strings.TrimSpace(instructions + "\n\n" + m.Instructions)
	if m.Personality != "" {
		instructions = strings.TrimSpace(instructions + "\n\nYour personality, which is how you write, never what you may do: " + m.Personality)
	}
	seat := core.Role{Name: m.Name, Kinds: kinds, Engine: m.Engine, Model: m.Model, Effort: m.Effort, Member: m.ID, Instructions: instructions}
	if seat.Holds(core.RoleQA) {
		seat.Browser = m.Browser
	}
	return seat
}

// joinSeat gives a member's seat on the team one more kind of role; taking
// QA brings the member's browser setting with it.
func joinSeat(playbook *core.Playbook, seat int, m core.Member, kind string) {
	playbook.Rekind(seat, slices.Concat(playbook.Roles[seat].Kinds, []string{kind}))
	if kind == core.RoleQA {
		playbook.Roles[seat].Browser = m.Browser
	}
}

// SetTeam applies a team choice made in the dashboard or by the assistant.
func (lp *Loop) SetTeam(ctx context.Context, projectID string, in TeamChoice) (core.Project, error) {
	p, err := lp.Core.EditPlaybook(ctx, projectID, func(snap *core.Snapshot, p *core.Project, pb *core.Playbook) error {
		playbook, err := chosenTeam(in, *snap, *p)
		if err != nil {
			return err
		}
		*pb = playbook
		return nil
	})
	// Queued work may have been waiting on a team, so look again now.
	if err == nil {
		lp.Nudge()
	}
	return p, err
}

// chosenTeam is the team a choice makes for a project, keeping what the
// choice doesn't cover from the team it has.
func chosenTeam(in TeamChoice, snap core.Snapshot, p core.Project) (core.Playbook, error) {
	playbook, err := teamFrom(in, snap, p.Playbook)
	if err != nil {
		return core.Playbook{}, err
	}
	// A member who stays in the same seat keeps the copy the team has; changes
	// to the member since reach this team when it is given the seat again.
	if p.Playbook != nil && p.Playbook.Template == playbook.Template {
		for k, r := range playbook.Roles {
			i := slices.IndexFunc(p.Playbook.Roles, func(c core.Role) bool { return r.Member != "" && c.Member == r.Member })
			if i >= 0 && sameKinds(p.Playbook.Roles[i].Kinds, r.Kinds) {
				playbook.Roles[k] = p.Playbook.Roles[i]
			}
		}
	}
	// The template's seats are made afresh, so one named like a member gives
	// way again, as it did when the member was given its seat.
	playbook.NameSeats()
	// How much of the project's work runs at once is its own setting, and
	// so is how much each stage holds.
	if p.Playbook != nil {
		playbook.MaxActive = p.Playbook.MaxActive
		playbook.StageLimits = maps.Clone(p.Playbook.StageLimits)
	}
	if playbook.Medium == core.MediumGit {
		if playbook.Repo, err = teamRepo(p, playbook.Repo); err != nil {
			return core.Playbook{}, err
		}
		// Choosing a team never changes where its work lands or how QA runs
		// the app; those are settings of their own.
		if p.Playbook != nil && p.Playbook.Medium == core.MediumGit {
			playbook.Land = p.Playbook.Land
			playbook.Run = p.Playbook.Run
			if in.CheckInCopy == "" {
				playbook.CheckInCopy = p.Playbook.CheckInCopy
			}
			if in.CheckLoopback == "" {
				playbook.CheckLoopback = p.Playbook.CheckLoopback
			}
		}
	}
	if in.Run != nil {
		recipe := in.Run.Trimmed()
		playbook.Run = &recipe
	}
	return playbook, nil
}

// editPlaybook changes the team a project has, in one step. codeOnly, when
// set, is what to say if the team doesn't work on code.
func (lp *Loop) editPlaybook(ctx context.Context, projectID, codeOnly string, change func(snap *core.Snapshot, p *core.Project, pb *core.Playbook) error) (core.Project, error) {
	return lp.Core.EditPlaybook(ctx, projectID, func(snap *core.Snapshot, p *core.Project, pb *core.Playbook) error {
		if codeOnly != "" && (p.Playbook == nil || p.Playbook.Medium != core.MediumGit) {
			return errors.New(codeOnly)
		}
		if p.Playbook == nil {
			return errors.New("choose a team first")
		}
		return change(snap, p, pb)
	})
}

// teamRepo is the repository a code team works on: one of the project's own
// folders, never an arbitrary path. Empty means the first.
func teamRepo(p core.Project, repo string) (string, error) {
	if repo == "" && len(p.Directories) > 0 {
		repo = p.Directories[0]
	}
	if !slices.Contains(p.Directories, repo) {
		return "", errors.New("a code team works on one of the project's linked folders; link the repository first")
	}
	return repo, nil
}

// sameKinds reports whether two seats hold the same kinds, in any order.
func sameKinds(a, b []string) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(k string) bool { return !slices.Contains(b, k) })
}

// SetSeat gives one kind of role to a member, or with no member back to the
// template's seat for it; NoResearcher as the researcher leaves research
// out, and as the designer leaves the team without one. A member already on
// the team takes the role in the seat it has. A role held in several seats
// is given as many again, filled like the first. Every other seat keeps the
// copy it has, and requests under way keep the team they started with.
func (lp *Loop) SetSeat(ctx context.Context, projectID, kind, memberID string) (core.Project, error) {
	p, err := lp.editPlaybook(ctx, projectID, "", func(snap *core.Snapshot, _ *core.Project, pb *core.Playbook) error {
		return giveSeat(pb, kind, memberID, *snap)
	})
	// Queued work may have been waiting on a researcher or a PM, so look
	// again.
	if err == nil {
		lp.Nudge()
	}
	return p, err
}

// giveSeat is the change SetSeat makes to a team.
func giveSeat(playbook *core.Playbook, kind, memberID string, snap core.Snapshot) error {
	base, templated := playbook.TemplateSeat(kind)
	// No template has a PM or a designer, so only they may join a team
	// without a seat for it; research can be left out only where the
	// template researches.
	if kind == core.RoleDesigner && memberID == NoResearcher {
		memberID = ""
	}
	if !templated && (!memberOnly(kind) || memberID == NoResearcher) {
		return fmt.Errorf("a %s team has no %s", playbook.Template, kind)
	}
	if memberID == NoResearcher && kind != core.RoleResearcher {
		return fmt.Errorf("only research can be left out of a team")
	}
	// The role keeps as many seats as it had, so the work it runs at once
	// stays as the owner set it: Claudius and Claudius #2 given back to the
	// template become Implementer and Implementer #2.
	seats := 0
	for _, r := range playbook.Roles {
		if r.Holds(kind) {
			seats++
		}
	}
	at := playbook.Unseat(kind)
	filled := ""
	switch memberID {
	case NoResearcher:
	case "":
		if templated {
			playbook.Roles = slices.Insert(playbook.Roles, at, base)
			filled = base.Name
		}
	default:
		m, err := memberFor(kind, memberID, snap)
		if err != nil {
			return err
		}
		if seat := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Member == m.ID }); seat >= 0 {
			joinSeat(playbook, seat, m, kind)
			filled = playbook.Roles[seat].Name
			break
		}
		playbook.Roles = slices.Insert(playbook.Roles, at, memberSeat(m, []string{kind}, base.Instructions))
		playbook.NameSeats()
		filled = playbook.Roles[slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Member == m.ID })].Name
	}
	for ; filled != "" && seats > 1; seats-- {
		if _, err := playbook.AddSeat(filled); err != nil {
			return err
		}
	}
	return nil
}

// AddSeat adds another seat filled like the named one: Claudius gains
// Claudius #2, then Claudius #3. Each seat takes one step at a time, so a
// project runs as many steps of a kind at once as it has free seats for
// it. Requests under way keep the team they started with.
func (lp *Loop) AddSeat(ctx context.Context, projectID, seat string) (core.Project, error) {
	return lp.changeSeats(ctx, projectID, func(playbook *core.Playbook) error {
		_, err := playbook.AddSeat(seat)
		return err
	})
}

// RemoveSeat takes the named seat off a project's team. The team must
// still have an implementer and a reviewer.
func (lp *Loop) RemoveSeat(ctx context.Context, projectID, seat string) (core.Project, error) {
	return lp.changeSeats(ctx, projectID, func(playbook *core.Playbook) error {
		return playbook.RemoveSeat(seat)
	})
}

func (lp *Loop) changeSeats(ctx context.Context, projectID string, change func(*core.Playbook) error) (core.Project, error) {
	p, err := lp.editPlaybook(ctx, projectID, "", func(_ *core.Snapshot, _ *core.Project, pb *core.Playbook) error {
		return change(pb)
	})
	// A seat added may be free for work waiting on one.
	if err == nil {
		lp.Nudge()
	}
	return p, err
}

// SetParallel sets how many of a project's tasks may be under way at once:
// 1 or more, or 0 for one per implementer seat. Tasks past it stay on the
// to-do list, in order, until one finishes or waits on the owner.
func (lp *Loop) SetParallel(ctx context.Context, projectID string, maxActive int) (core.Project, error) {
	return lp.changeSeats(ctx, projectID, func(playbook *core.Playbook) error {
		playbook.MaxActive = maxActive
		return nil
	})
}

// SetStageLimits sets the most tasks each working stage of a project's
// board may hold at once, keyed by stage; a stage left out, or 0, has no
// limit. A task that finishes a stage waits in it for room in the next.
func (lp *Loop) SetStageLimits(ctx context.Context, projectID string, limits map[string]int) (core.Project, error) {
	return lp.changeSeats(ctx, projectID, func(playbook *core.Playbook) error {
		playbook.StageLimits = nil
		for stage, limit := range limits {
			if limit != 0 {
				if playbook.StageLimits == nil {
					playbook.StageLimits = map[string]int{}
				}
				playbook.StageLimits[stage] = limit
			}
		}
		return nil
	})
}

// UseProjectTeam moves a task that waits for the owner onto the team its
// project has now; see core.Service.UseProjectTeam.
func (lp *Loop) UseProjectTeam(ctx context.Context, projectID, taskID string) (core.Task, error) {
	t, err := lp.Core.UseProjectTeam(ctx, projectID, taskID)
	if err == nil {
		lp.Nudge()
	}
	return t, err
}
