package work

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

func (lp *Loop) validateAcceptedMerge(ctx context.Context, p core.Project, t core.Task, m medium) error {
	if !t.AcceptanceStands(p.Brief.Version) {
		return nil
	}
	v := t.MergeValidation
	if v == nil {
		return nil
	}
	if v.Failure != "" {
		return lp.acceptedMergeFailure(ctx, t, v.Failure)
	}
	if !t.MergeCheckPending(p.Brief.Version) {
		return nil
	}
	r := t.Revisions[len(t.Revisions)-1]
	evidence, failure, checkErr := lp.runRevisionCheck(ctx, p, t, m)

	if checkErr != nil {
		if taskPlaybook(p, t).ScopedCheck != "" {
			if errors.Is(checkErr, errCommandRecovery) {
				_, err := lp.updateOpen(ctx, t.ID, func(now *core.Task, current *core.Project) (string, error) {
					if !now.MergeCheckPending(current.Brief.Version) || current.Brief.Version != p.Brief.Version || now.TextVersion != t.TextVersion || now.MergeValidation.Ref != r.Ref || now.MergeValidation.Revision != r.N {
						return "", core.ErrStale
					}
					pending := *now.MergeValidation
					pending.Evidence = text.Clip(failure, 2000)
					now.MergeValidation = &pending
					now.Detail = "Project check cleanup is unconfirmed"
					return now.Detail, nil
				})
				return errors.Join(checkErr, err)
			}
			return lp.retryProjectCheck(ctx, t, checkErr)
		}
		// Repositories without scoped checks retain their existing accepted-merge
		// failure decisions, including the cleanup hold.
		if errors.Is(checkErr, errCommandRecovery) {
			return errors.Join(checkErr, lp.acceptedMergeFailure(ctx, t, failure))
		}
		failure = checkErr.Error()
	}

	_, err := lp.updateOpen(ctx, t.ID, func(now *core.Task, current *core.Project) (string, error) {
		if !now.AcceptanceStands(current.Brief.Version) || now.TextVersion != t.TextVersion || current.Brief.Version != p.Brief.Version || len(now.Revisions) == 0 || now.Revisions[len(now.Revisions)-1].Ref != r.Ref || now.MergeValidation == nil || now.MergeValidation.Revision != r.N {
			return "", core.ErrStale
		}
		if taskPlaybook(p, t).ScopedCheck != "" {
			now.Failures, now.RetryAt = 0, time.Time{}
		}
		now.MergeValidation = &core.MergeValidation{Revision: r.N, Ref: r.Ref, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Checked: true, Failure: text.Clip(failure, 2000), Evidence: evidence}
		return fmt.Sprintf("Checked accepted draft %d of %s after catch-up", r.N, now.Objective), nil
	})
	if err != nil {
		return err
	}
	if failure != "" {
		return lp.acceptedMergeFailure(ctx, t, text.Clip(failure, 2000))
	}
	return nil
}

// runRevisionCheck is shared by accepted catch-up validation and the pre-approval gate.
func (lp *Loop) runRevisionCheck(ctx context.Context, p core.Project, t core.Task, m medium) (string, string, error) {
	r := t.Revisions[len(t.Revisions)-1]
	evidence, failure := "No project check configured.", ""
	if command := taskPlaybook(p, t).Check; strings.TrimSpace(command) != "" {
		gm, ok := m.(gitMedium)
		if !ok {
			return "", "", errors.New("project validation requires a git workspace")
		}
		c, err := gm.check(ctx, t, r, false, false)
		if err != nil {
			return "", "", err
		} else {
			cleanupHeld := false
			defer func() {
				if !cleanupHeld {
					c.remove()
				}
			}()
			var cleanupErr error
			var cleanupCopy func()
			var coverageErr error
			batch := 0
			label := "Accepted merged draft"
			if t.FullCheckPending(p.Brief.Version) {
				label = "Full check of draft"
			}
			turn := fmt.Sprintf("accepted-check-%d-%d", r.N, time.Now().UnixNano())
			result, progress, runErr := lp.hostedCheck(ctx, gm, c.checkDir, "", gm.playbook.Check, func(err error, remove func()) {
				cleanupErr, cleanupCopy = err, remove
				lp.commandCleanup(err)
			}, func(progress *checktest.Progress, status string) {
				noteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				noteCoverage(progress, status, func(note string) {
					batch++
					coverageErr = errors.Join(coverageErr, lp.Core.RecordTurnStep(noteCtx, core.TurnStep{
						TaskID: t.ID, Seat: "Project check", Role: core.RoleQA, Turn: turn,
						Item: fmt.Sprint(batch), Kind: core.StepNote, At: time.Now().UTC(),
						Text: fmt.Sprintf("%s %d (%s), brief %d, requirements %d.\n%s", label, r.N, r.Ref, p.Brief.Version, t.TextVersion, note),
					}))
				})
			})
			if cleanupErr != nil {
				cleanupHeld = true
				lp.keepCommandCleanup(ctx, t.ID, func() {
					c.remove()
					if cleanupCopy != nil {
						cleanupCopy()
					}
				})
				failure = "Project check cleanup is unconfirmed: " + cleanupErr.Error()
				return evidence, failure, errors.Join(errCommandRecovery, cleanupErr)
			}
			evidence, _ = commandResultJSON(result)
			if err := errors.Join(runErr, cleanupErr, coverageErr); err != nil {
				return evidence, "", err
			} else if result.ExitCode != 0 || result.TimedOut || (gm.playbook.ScopedCheck != "" && !fullCoveragePassed(progress)) {
				diagnostic, _ := commandTail(result.Stderr, 900)
				output, _ := commandTail(result.Stdout, 500)
				if gm.playbook.ScopedCheck != "" && !fullCoveragePassed(progress) {
					diagnostic += " Full check coverage is missing, incomplete, or has required skips."
				}
				failure = fmt.Sprintf("Project check failed: exit_code=%d timed_out=%t\nDiagnostic: %s\nOutput: %s", result.ExitCode, result.TimedOut, diagnostic, output)
			}
		}
	}
	return evidence, failure, nil
}

// Persist the reason before opening its decision, so recovery cannot fall
// through to review routing or round-limit escalation after a failed check.
func (lp *Loop) acceptedMergeFailure(ctx context.Context, t core.Task, reason string) error {
	n := t.Revisions[len(t.Revisions)-1].N
	if t.Acceptance == nil {
		return core.ErrStale
	}
	_, err := lp.Core.FailAcceptedCatchUp(ctx, t.ID, n, t.Acceptance.BriefVersion, t.Acceptance.TextVersion, false, core.DecisionInput{
		Title:          fmt.Sprintf("Checks failed on merged draft %d of “%s”", n, t.Objective),
		Context:        fmt.Sprintf("The accepted draft was caught up without conflicts, but validation of merged draft %d failed: %s", n, text.Clip(reason, 2000)),
		Recommendation: choiceTryAgain + " after fixing the cause, or give the team direction",
		Choices:        []string{choiceTryAgain, choiceStop},
	})
	return err
}

// Put the blocker before bounded optional prose so clipping preserves it.
func acceptedVerdictFailure(v core.Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\nQuestion: %s\nSummary: %s", text.Clip(v.Role, 100), v.Outcome, text.Clip(v.Question, 800), text.Clip(v.Summary, 400))
	for _, f := range v.Findings {
		if b.Len() >= 1500 {
			break
		}
		fmt.Fprintf(&b, "\n- %s", text.Clip(f.Note, 200))
	}
	fmt.Fprintf(&b, "\nNote: %s", text.Clip(v.Note, 200))
	return b.String()
}

func fullCoveragePassed(p *checktest.Progress) bool {
	// Generic checks need not implement crew coverage reporting. When present,
	// the report must establish complete coverage without required skips.
	if p == nil {
		return true
	}
	if !p.Done || p.Stage != "complete" || !p.Report.Complete || p.Report.Failed {
		return false
	}
	for _, skip := range p.Report.Skips {
		if skip.Exception == "" {
			return false
		}
	}
	return true
}

// Infrastructure retries do not consume draft rounds. Cleanup retains its
// claim until the existing recovery loop confirms that commands have settled.
func (lp *Loop) retryProjectCheck(ctx context.Context, t core.Task, err error) error {
	if errors.Is(err, errCommandRecovery) || errors.Is(err, core.ErrStale) || ctx.Err() != nil {
		return err
	}
	return lp.roleFailed(ctx, t, "The project check", err)
}
