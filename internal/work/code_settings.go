package work

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

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
	return lp.editPlaybook(ctx, projectID, "a workspace is for code teams; choose a code team first", func(_ *core.Snapshot, p *core.Project, playbook *core.Playbook) error {
		repo, err := teamRepo(*p, in.Repo)
		if err != nil {
			return err
		}
		playbook.Repo = repo
		playbook.BranchPrefix = strings.TrimSpace(in.BranchPrefix)
		if playbook.BranchPrefix == "" {
			playbook.BranchPrefix = core.Templates[playbook.Template].BranchPrefix
		}
		playbook.Prepare = append([]string(nil), in.Prepare...)
		playbook.Sign = in.Sign
		return nil
	})
}

// SetLanding sets what landing means for a code project. The owner and the
// assistant can; nothing inside the project can. Tasks already under way keep
// the policy they started with; turning pull requests off has someone choose
// whether those that started with them carry on with them.
func (lp *Loop) SetLanding(ctx context.Context, projectID string, land core.LandPolicy) (core.Project, error) {
	p, err := lp.editPlaybook(ctx, projectID, "landing policies are for code teams; choose a code team first", func(_ *core.Snapshot, _ *core.Project, playbook *core.Playbook) error {
		land.Means, land.Target, land.GitHub = strings.TrimSpace(land.Means), strings.TrimSpace(land.Target), strings.TrimSpace(land.GitHub)
		playbook.Land = land
		return nil
	})
	if err != nil || land.PullRequests {
		return p, err
	}
	if err = lp.Core.EndPullRequests(ctx, projectID); err != nil {
		return p, err
	}
	lp.Nudge()
	return p, nil
}

// SetRunRecipe sets how QA starts a code project to use it, or with nil
// takes the recipe away, so QA only runs the check. The owner and the
// assistant can; the team only proposes one. Tasks already under way keep
// the recipe they started with.
func (lp *Loop) SetRunRecipe(ctx context.Context, projectID string, recipe *core.RunRecipe) (core.Project, error) {
	return lp.editPlaybook(ctx, projectID, "run recipes are for code teams; choose a code team first", func(_ *core.Snapshot, _ *core.Project, playbook *core.Playbook) error {
		playbook.Run = nil
		if recipe != nil {
			trimmed := recipe.Trimmed()
			playbook.Run = &trimmed
		}
		return nil
	})
}

// SetSeatBrowser sets whether the team's QA uses its engine's own browser,
// and which connected one, for this project only; the member it was copied
// from keeps its own setting.
func (lp *Loop) SetSeatBrowser(ctx context.Context, projectID string, browser core.Browser) (core.Project, error) {
	return lp.editPlaybook(ctx, projectID, "", func(_ *core.Snapshot, _ *core.Project, playbook *core.Playbook) error {
		k := slices.IndexFunc(playbook.Roles, func(r core.Role) bool { return r.Holds(core.RoleQA) })
		if k < 0 {
			return errors.New("this team has no QA")
		}
		playbook.Roles[k].Browser = core.Browser{On: browser.On, Name: strings.TrimSpace(browser.Name)}
		return nil
	})
}
