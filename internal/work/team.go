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
}

// teamFrom builds a playbook from a template and the few choices the assistant
// may make about it. Anything left empty keeps the template's choice.
func teamFrom(in TeamChoice, snap core.Snapshot) (core.Playbook, error) {
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
		case playbook.Roles[i].Kind == core.RoleImplementer && in.WriterEngine != "":
			playbook.Roles[i].Engine = in.WriterEngine
		case playbook.Roles[i].Kind == core.RoleReviewer && in.ReviewerEngine != "":
			playbook.Roles[i].Engine = in.ReviewerEngine
		}
	}
	for _, slot := range [][2]string{{core.RoleImplementer, in.Implementer}, {core.RoleReviewer, in.Reviewer}, {core.RoleQA, in.QA}} {
		if slot[1] == "" {
			continue
		}
		if err := fillRole(&playbook, slot[0], slot[1], snap); err != nil {
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

// fillRole puts a member in the template's role of that kind. The template's
// instructions stay, since they say how this kind of work is done here; the
// member's own follow them.
func fillRole(playbook *core.Playbook, kind, id string, snap core.Snapshot) error {
	m, ok := snap.Member(id)
	if !ok {
		return fmt.Errorf("there is no team member %q: %w", id, core.ErrNotFound)
	}
	if m.Kind != kind {
		return fmt.Errorf("%s is a %s, not a %s", m.Name, m.Kind, kind)
	}
	slot := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Kind == kind })
	if slot < 0 {
		return fmt.Errorf("a %s team has no %s for %s to fill", playbook.Template, kind, m.Name)
	}
	r := &playbook.Roles[slot]
	r.Name, r.Engine, r.Model, r.Effort, r.Member = m.Name, m.Engine, m.Model, m.Effort, m.ID
	r.Instructions = strings.TrimSpace(r.Instructions + "\n\n" + m.Instructions)
	return nil
}

// SetTeam applies a team choice made in the dashboard or by the assistant.
func (lp *Loop) SetTeam(ctx context.Context, projectID string, in TeamChoice) (core.Project, error) {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return core.Project{}, err
	}
	playbook, err := teamFrom(in, snap)
	if err != nil {
		return core.Project{}, err
	}
	if playbook.Medium == core.MediumGit {
		p, ok := findProject(snap, projectID)
		if !ok {
			return core.Project{}, core.ErrNotFound
		}
		// A code team works on one of the project's own folders, never an
		// arbitrary path.
		if playbook.Repo == "" && len(p.Directories) > 0 {
			playbook.Repo = p.Directories[0]
		}
		if !slices.Contains(p.Directories, playbook.Repo) {
			return core.Project{}, errors.New("a code team works on one of the project's linked folders; link the repository first")
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
