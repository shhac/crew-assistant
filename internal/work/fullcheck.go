package work

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

func (lp *Loop) validateFullCheck(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if !t.FullCheckPending(p.Brief.Version) {
		return nil
	}
	r := t.Revisions[len(t.Revisions)-1]
	evidence, failure, runErr := lp.runRevisionCheck(ctx, p, t, m)

	if runErr != nil {
		if errors.Is(runErr, errCommandRecovery) {
			_, err := lp.updateOpen(ctx, t.ID, func(now *core.Task, current *core.Project) (string, error) {
				if !now.FullCheckPending(current.Brief.Version) || now.FullCheck.Ref != r.Ref || now.FullCheck.Revision != r.N || current.Brief.Version != p.Brief.Version || now.TextVersion != t.TextVersion || now.DirectionPending > 0 {
					return "", core.ErrStale
				}
				pending := *now.FullCheck
				pending.Failure = text.Clip(failure, 2000)
				now.FullCheck = &pending
				return "Project check cleanup is unconfirmed", nil
			})
			return errors.Join(runErr, err)
		}
		return lp.retryProjectCheck(ctx, t, runErr)
	}

	_, err := lp.updateOpen(ctx, t.ID, func(now *core.Task, current *core.Project) (string, error) {
		if current.Brief.Version != p.Brief.Version || now.TextVersion != t.TextVersion || !now.FullCheckPending(current.Brief.Version) || now.FullCheck.Ref != r.Ref || now.FullCheck.Revision != r.N || now.DirectionPending > 0 {
			return "", core.ErrStale
		}
		now.Failures, now.RetryAt = 0, time.Time{}
		now.FullCheck = &core.MergeValidation{Revision: r.N, Ref: r.Ref, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Checked: true, Failure: text.Clip(failure, 2000), Evidence: text.Clip(evidence, 32000)}
		return fmt.Sprintf("Ran the full check on draft %d of %s", r.N, now.Objective), nil
	})
	if err != nil {
		return errors.Join(runErr, err)
	}
	if failure != "" && t.AcceptanceStands(p.Brief.Version) {
		return lp.acceptedMergeFailure(ctx, t, failure)
	}
	return nil
}
