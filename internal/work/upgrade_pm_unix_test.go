//go:build !windows

package work

import (
	"context"
	"errors"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"testing"
	"time"
)

func TestLandingAndEscalationExposeBlockedPMTurns(t *testing.T) {
	for _, kind := range []string{"landing", "escalation"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			w := newPMPush(t, core.ApprovePM, "")
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Ready to land"})
			if err != nil {
				t.Fatal(err)
			}
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(task *core.Task, p *core.Project) (string, error) {
				task.Status = core.TaskDeciding
				task.Playbook = p.Playbook
				task.Roles = p.Playbook.Roles
				task.Round = 1
				task.MaxRounds = 3
				task.Revisions = []core.Revision{{N: 1, Ref: "fixture", BriefVersion: p.Brief.Version}}
				for _, checker := range task.Checkers() {
					task.Verdicts = append(task.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			w.a.runner = pmChatRunner{run: func(ctx context.Context, s roles.Spec) (roles.Result, error) {
				if s.Observer == nil {
					t.Error("unobserved PM entry point")
					close(entered)
					return roles.Result{}, errors.New("no observer")
				}
				s.Observer.Started()
				defer s.Observer.Ended()
				close(entered)
				<-release
				return roles.Result{}, context.Canceled
			}}
			done := make(chan struct{})
			defer func() { close(release); <-done }()
			go func() {
				defer close(done)
				if kind == "landing" {
					_ = w.a.pmLanding(ctx, w.p, task, task.Revisions[0], &blockedDeliveryMedium{})
				} else {
					_, _, _ = w.a.judgeEscalation(ctx, w.p, task, "fixture", 1)
				}
			}()
			select {
			case <-entered:
			case <-done:
				t.Fatal("PM session did not start")
			case <-time.After(3 * time.Second):
				t.Fatal("PM did not enter")
			}
			turns := w.a.Turns()
			if len(turns) != 1 || turns[0].Role != core.RolePM || turns[0].TaskID != task.ID {
				t.Fatal("automatic quiet/drain observation missed PM work", turns)
			}
		})
	}
}
