package work

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media"
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
		playbook.Rekind(seat, slices.Concat(playbook.Roles[seat].Kinds, []string{kind}))
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
// template's instructions for them.
func memberSeat(m core.Member, kinds []string, instructions string) core.Role {
	return core.Role{Name: m.Name, Kinds: kinds, Engine: m.Engine, Model: m.Model, Effort: m.Effort, Member: m.ID, Instructions: strings.TrimSpace(instructions + "\n\n" + m.Instructions)}
}

// SetTeam applies a team choice made in the dashboard or by the assistant.
func (lp *Loop) SetTeam(ctx context.Context, projectID string, in TeamChoice) (core.Project, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Project{}, err
	}
	var current *core.Playbook
	if p, ok := findProject(snap, projectID); ok {
		current = p.Playbook
	}
	playbook, err := teamFrom(in, snap, current)
	if err != nil {
		return core.Project{}, err
	}
	// A member who stays in the same seat keeps the copy the team has; changes
	// to the member since reach this team when it is given the seat again.
	if p, ok := findProject(snap, projectID); ok && p.Playbook != nil && p.Playbook.Template == playbook.Template {
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
	if playbook.Medium == core.MediumGit {
		p, ok := findProject(snap, projectID)
		if !ok {
			return core.Project{}, core.ErrNotFound
		}
		if playbook.Repo, err = teamRepo(p, playbook.Repo); err != nil {
			return core.Project{}, err
		}
		// Choosing a team never changes where its work lands; that is its own
		// setting.
		if p.Playbook != nil && p.Playbook.Medium == core.MediumGit {
			playbook.Land = p.Playbook.Land
		}
	}
	if err = playbook.Validate(); err != nil {
		return core.Project{}, err
	}
	// Queued work may have been waiting on a team, so look again now.
	p, err := lp.Core.SetPlaybook(ctx, projectID, playbook)
	if err == nil {
		lp.Nudge()
	}
	return p, err
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
// the team takes the role in the seat it has. Every other seat keeps the copy
// it has, and requests under way keep the team they started with.
func (lp *Loop) SetSeat(ctx context.Context, projectID, kind, memberID string) (core.Project, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Project{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Project{}, core.ErrNotFound
	}
	if p.Playbook == nil {
		return core.Project{}, errors.New("choose a team first")
	}
	playbook := *p.Playbook
	base, templated := playbook.TemplateSeat(kind)
	// No template has a PM or a designer, so only they may join a team
	// without a seat for it; research can be left out only where the
	// template researches.
	if kind == core.RoleDesigner && memberID == NoResearcher {
		memberID = ""
	}
	if !templated && (!memberOnly(kind) || memberID == NoResearcher) {
		return core.Project{}, fmt.Errorf("a %s team has no %s", playbook.Template, kind)
	}
	if memberID == NoResearcher && kind != core.RoleResearcher {
		return core.Project{}, fmt.Errorf("only research can be left out of a team")
	}
	at := playbook.Unseat(kind)
	switch memberID {
	case NoResearcher:
	case "":
		if templated {
			playbook.Roles = slices.Insert(playbook.Roles, at, base)
		}
	default:
		m, err := memberFor(kind, memberID, snap)
		if err != nil {
			return core.Project{}, err
		}
		if seat := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Member == m.ID }); seat >= 0 {
			playbook.Rekind(seat, slices.Concat(playbook.Roles[seat].Kinds, []string{kind}))
			break
		}
		playbook.Roles = slices.Insert(playbook.Roles, at, memberSeat(m, []string{kind}, base.Instructions))
		playbook.NameSeats()
	}
	if err = playbook.Validate(); err != nil {
		return core.Project{}, err
	}
	// Queued work may have been waiting on a researcher or a PM, so look
	// again.
	p, err = lp.Core.SetPlaybook(ctx, projectID, playbook)
	if err == nil {
		lp.Nudge()
	}
	return p, err
}

// Workspace is where a code team works and how its work is kept there.
type Workspace struct {
	Repo         string   `json:"repo"`
	BranchPrefix string   `json:"branch_prefix"`
	Prepare      []string `json:"prepare"`
	Sign         string   `json:"sign"`
}

// SetWorkspace changes where a code team works and nothing else: its roles,
// check and landing stay as they are.
func (lp *Loop) SetWorkspace(ctx context.Context, projectID string, in Workspace) (core.Project, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Project{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Project{}, core.ErrNotFound
	}
	if p.Playbook == nil || p.Playbook.Medium != core.MediumGit {
		return core.Project{}, errors.New("a workspace is for code teams; choose a code team first")
	}
	playbook := *p.Playbook
	if playbook.Repo, err = teamRepo(p, in.Repo); err != nil {
		return core.Project{}, err
	}
	playbook.BranchPrefix = strings.TrimSpace(in.BranchPrefix)
	if playbook.BranchPrefix == "" {
		playbook.BranchPrefix = core.Templates[playbook.Template].BranchPrefix
	}
	playbook.Prepare = append([]string(nil), in.Prepare...)
	playbook.Sign = in.Sign
	if err = playbook.Validate(); err != nil {
		return core.Project{}, err
	}
	return lp.Core.SetPlaybook(ctx, projectID, playbook)
}

// SetLanding sets what landing means for a code project. The owner and the
// assistant can; nothing inside the project can. Tasks already under way keep
// the policy they started with.
func (lp *Loop) SetLanding(ctx context.Context, projectID string, land core.LandPolicy) (core.Project, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Project{}, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.Project{}, core.ErrNotFound
	}
	if p.Playbook == nil || p.Playbook.Medium != core.MediumGit {
		return core.Project{}, errors.New("landing policies are for code teams; choose a code team first")
	}
	playbook := *p.Playbook
	land.Means, land.Target, land.GitHub = strings.TrimSpace(land.Means), strings.TrimSpace(land.Target), strings.TrimSpace(land.GitHub)
	playbook.Land = land
	if err = playbook.Validate(); err != nil {
		return core.Project{}, err
	}
	return lp.Core.SetPlaybook(ctx, projectID, playbook)
}

// RevisionPreview returns what one revision holds, for the owner to read.
func (lp *Loop) RevisionPreview(ctx context.Context, projectID, taskID string, n int) ([]media.File, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return nil, core.ErrNotFound
	}
	t, ok := findTask(snap, projectID, taskID)
	if !ok {
		return nil, core.ErrNotFound
	}
	for _, r := range t.Revisions {
		if r.N == n {
			m, err := lp.mediumFor(ctx, p, taskPlaybook(p, t))
			if err != nil {
				return nil, err
			}
			return m.preview(ctx, t, r)
		}
	}
	return nil, core.ErrNotFound
}
