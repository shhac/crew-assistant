package work

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
)

func runningBuild() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value, true
		}
	}
	return "", false
}

type blockerCheck struct {
	includes bool
	reason   string
	retry    time.Time
}

// checkBlockers makes observations before scheduling, with no delivery capability.
func (lp *Loop) checkBlockers(ctx context.Context, snap core.Snapshot) error {
	build := lp.Build
	if build == nil {
		build = runningBuild
	}
	commit, stamped := build()
	for _, t := range snap.Tasks {
		if snap.ProjectPaused(t.ProjectID) {
			continue
		}
		if t.Finished() {
			continue
		}
		for _, b := range t.Blockers {
			if b.Kind != core.BlockerDaemonIncludes || b.ClearedAt != nil {
				continue
			}
			target, ok := snap.FindTask(b.Task)
			reason := ""
			switch {
			case !ok:
				reason = "the named request is no longer here"
			case target.Status == core.TaskStopped:
				reason = "the named request was stopped"
			case target.Status != core.TaskLanded:
				continue
			case len(target.Revisions) == 0:
				reason = "the landed request has no recorded change"
			case !stamped:
				reason = "the running build doesn't record its commit"
			default:
				r := target.Revisions[len(target.Revisions)-1]
				key := fmt.Sprintf("%s/%d/%s/%s", target.ID, r.N, r.Ref, commit)
				cached, exists := lp.blockerChecks.Load(key)
				check := blockerCheck{}
				if exists {
					check = cached.(blockerCheck)
				}
				if !exists || (!check.retry.IsZero() && !time.Now().Before(check.retry)) {
					p, _ := findProject(snap, target.ProjectID)
					pb := taskPlaybook(p, target)
					includes := lp.Includes
					if includes == nil {
						includes = gitrepo.BuildIncludes
					}
					var err error
					if pb == nil {
						err = fmt.Errorf("no repository")
					} else {
						check.includes, err = includes(ctx, pb.Repo, commit, r.Ref, landedTrailer(target, r))
					}
					check.reason = "the running daemon does not include the landed change"
					check.retry = time.Time{}
					if err != nil {
						check.reason = "the landed change could not be checked in the repository"
						check.retry = time.Now().Add(time.Minute)
					}
					lp.blockerChecks.Store(key, check)
				}
				if check.includes {
					if err := lp.Core.ClearDaemonBlocker(ctx, t.ID, b.ID, target.ID, r.N); err != nil {
						return err
					}
					continue
				}
				reason = check.reason
			}
			if b.Check == reason {
				continue
			}
			if err := lp.Core.CheckBlocker(ctx, t.ID, b.ID, reason); err != nil {
				return err
			}
		}
	}
	return nil
}

func (lp *Loop) SetBlocker(ctx context.Context, in core.BlockerInput) (core.Task, error) {
	t, err := lp.Core.SetBlocker(ctx, in)
	lp.nudgeUnless(err)
	return t, err
}
func (lp *Loop) ClearBlocker(ctx context.Context, project, task, id, by, why string) (core.Task, error) {
	t, err := lp.Core.ClearBlocker(ctx, project, task, id, by, why)
	lp.nudgeUnless(err)
	return t, err
}
