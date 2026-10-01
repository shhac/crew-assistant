package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

func (a *App) LinearOptions(ctx context.Context, id, profile, kind, team string) (any, error) {
	if a.Demo {
		return nil, errors.New("demo mode does not query external accounts")
	}
	if err := a.refuseWhileStopping(); err != nil {
		return nil, err
	}
	return a.connectionClient.LinearOptions(ctx, a.Config().Connections, id, profile, kind, team)
}
func (a *App) syncProjectLinear(ctx context.Context) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, p := range snap.Projects {
		if p.Linear == nil || !p.Linear.Rules.PickUp {
			continue
		}
		l := *p.Linear
		imported := map[string]bool{}
		for _, id := range p.LinearImported {
			imported[id] = true
		}
		pages := []connections.LinearPage{}
		after := l.Cursor
		seen := map[string]bool{}
		if after != "" {
			seen[after] = true
		}
		more := false
		resetCursor := false
		bindings := a.Config().Connections
		err = core.TaskQueueReady(p)
		var session *connections.LinearSession
		if err == nil {
			session, err = a.connectionClient.LinearAccount(ctx, bindings, l.ConnectionID, l.Profile)
		}
		// Reserve one of the five reads for recent matches when resuming.
		// Its end cursor must never replace the saved backlog position.
		headPages := 0
		if err == nil && l.Cursor != "" {
			var head connections.LinearPage
			head, err = session.PickUpIssues(ctx, l, "")
			if err == nil {
				pages = append(pages, head)
				headPages = 1
			}
		}
		for page := 0; page < 5-headPages && err == nil; page++ {
			var result connections.LinearPage
			result, err = session.PickUpIssues(ctx, l, after)
			if err != nil {
				resetCursor = page == 0 && l.Cursor != ""
				break
			}
			pages = append(pages, result)
			more = result.PageInfo.HasNextPage
			if !more {
				break
			}
			next := result.PageInfo.EndCursor
			if next == "" || seen[next] {
				err = errors.New("Linear pagination did not advance")
				break
			}
			after = next
			seen[next] = true
		}
		count := 0
		cursor := l.Cursor
		// Read all new issue context before any insert: a failed read imports nothing.
		if err == nil {
			for page := range pages {
				for node := range pages[page].Nodes {
					issue := &pages[page].Nodes[node]
					if imported[issue.ID] {
						continue
					}
					issue.Description, err = session.LinearIssueContext(ctx, l, *issue)
					if err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
		}
		if resetCursor {
			if resetErr := a.Core.SetProjectLinearCursor(ctx, p.ID, l.Version, ""); resetErr != nil {
				err = resetErr
			}
		}
		if err == nil {
			for pageIndex, page := range pages {
				complete := true
				for n, i := range page.Nodes {
					if imported[i.ID] {
						continue
					}
					var made bool
					made, err = a.Core.PickUpLinearIssue(ctx, p.ID, l.Version, i)
					if err != nil {
						complete = false
						break
					}
					imported[i.ID] = true
					if made {
						count++
					}
					if count == 50 {
						complete = n == len(page.Nodes)-1
						break
					}
				}
				if err != nil {
					break
				}
				if complete && pageIndex >= headPages {
					cursor = ""
					if page.PageInfo.HasNextPage {
						cursor = page.PageInfo.EndCursor
					}
				}
				if count == 50 {
					more = !complete || page.PageInfo.HasNextPage
					break
				}
			}
			if err == nil {
				err = a.Core.SetProjectLinearCursor(ctx, p.ID, l.Version, cursor)
			}
		}
		message := ""
		state := "connected"
		detail := fmt.Sprintf("%d issues picked up", count)
		if more {
			detail += "; more may be waiting"
		}
		if errors.Is(err, core.ErrConflict) {
			a.Status("linear-project:"+p.ID, p.Title+" Linear pick-up", "connected", "Settings changed; skipped")
			continue
		}
		if err != nil {
			message = err.Error()
			state = "error"
			detail = message
			failures = append(failures, err)
		}
		_ = a.Core.LinearError(ctx, p.ID, l.Version, message)
		a.Status("linear-project:"+p.ID, p.Title+" Linear pick-up", state, detail)
	}
	return errors.Join(failures...)
}

func (a *App) LinkTaskLinear(ctx context.Context, projectID, taskID, connectionID, profile, kind, input, by string) (core.Task, error) {
	if a.Demo {
		return core.Task{}, errors.New("demo mode does not query external accounts")
	}
	if err := a.refuseWhileStopping(); err != nil {
		return core.Task{}, err
	}
	if kind != "issue" && kind != "project" {
		return core.Task{}, errors.New("choose a Linear issue or project")
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return core.Task{}, err
	}
	var found bool
	for _, t := range snap.Tasks {
		if (t.ID == taskID || t.Ref == taskID) && t.ProjectID == projectID {
			found = true
		}
	}
	if !found {
		return core.Task{}, core.ErrNotFound
	}
	if connectionID == "" && profile == "" {
		for _, p := range snap.Projects {
			if p.ID == projectID && p.Linear != nil {
				connectionID, profile = p.Linear.ConnectionID, p.Linear.Profile
			}
		}
	}
	session, err := a.connectionClient.LinearAccount(ctx, a.Config().Connections, connectionID, profile)
	if err != nil {
		return core.Task{}, err
	}
	var ref core.LinearRef
	switch kind {
	case "issue":
		ref, err = session.LinearIssueRef(ctx, input)
	case "project":
		ref, err = session.LinearProjectRef(ctx, input)
	}
	if err != nil {
		return core.Task{}, err
	}
	if err := a.refuseWhileStopping(); err != nil {
		return core.Task{}, err
	}
	return a.Core.AddTaskLinear(ctx, projectID, taskID, ref, by)
}
func (a *App) UnlinkTaskLinear(ctx context.Context, projectID, taskID, kind, id, by string) (core.Task, error) {
	if a.Demo {
		return core.Task{}, errors.New("demo mode does not change external links")
	}
	if err := a.refuseWhileStopping(); err != nil {
		return core.Task{}, err
	}
	return a.Core.RemoveTaskLinear(ctx, projectID, taskID, kind, id, by)
}
