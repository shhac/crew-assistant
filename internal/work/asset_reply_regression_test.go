//go:build !windows

package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

func TestKeptNoChangePRReplyPreservesBookkeeping(t *testing.T) {
	t.Parallel()
	for _, seen := range []int{0, 1} {
		t.Run(fmt.Sprint(seen), func(t *testing.T) {
			t.Parallel()
			s := newPRScenario(t, 2)
			task := s.open(t)
			var err error
			task, err = s.a.Core.UpdateTask(s.ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames"}
				t.TeamKept = []string{"Frames"}
				t.Direction = []string{"owner request"}
				t.DirectionPending = 1
				t.Messages = []core.TeamMessage{{ID: "owner-message", Kind: core.RoleImplementer, Status: core.MessageWaiting, Direction: 0}}
				t.WriterNext = "resume"
				t.WriterRequest = 3
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := s.a.testMedium(t, s.p.ID, task.ID)
			if err = m.reset(s.ctx, task); err != nil {
				t.Fatal(err)
			}
			writer := task.RolesOf(core.RoleImplementer)[0]
			reply := "Nothing to change.\n```owner-step\n" + `[{"requirement":"Frames","why":"code","asset_creation":false}]` + "\n```\n```pr-reply\n" + `{"replies":[{"thread":"T1","body":"As intended."}]}` + "\n```\n```wake\ninvalid\n```"
			session := []byte(`{"engine":"claude","id":"kept-reply"}`)
			if err = s.a.recordDraft(s.ctx, s.p, task, m, writer.Name, roles.Result{Text: reply, Session: session}, seen, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := s.a.Core.Snapshot(s.ctx)
			got, _ := snap.FindTask(task.ID)
			want := core.TaskLanding
			if seen == 0 {
				want = core.TaskWriting
			}
			thread, ok := got.Thread(core.RoleImplementer, writer)
			if got.Status != want || len(got.Revisions) != 1 || len(got.Proposal.Outbox) != 1 || got.Proposal.Outbox[0].Revision != 1 || got.WriterNext != "" || len(got.WakeErrors) == 0 || !ok || string(thread.Session) != string(session) {
				t.Fatal(got)
			}
			if seen == 1 && (got.DirectionPending != 0 || got.Messages[0].Status != core.MessageAnswered || !strings.Contains(got.Messages[0].Reply, "Nothing to change")) {
				t.Fatal(got)
			}
			if seen == 0 && (got.DirectionPending != 1 || got.Messages[0].Status != core.MessageWaiting) {
				t.Fatal(got)
			}
		})
	}
}

func TestDeliveredKeptReportCannotReopenThroughDraftAndHandoff(t *testing.T) {
	t.Parallel()
	a, _, p, task := ownerAssetFixture(t)
	ctx := context.Background()
	original := task.Design[0].AssetReports[0]
	for turn := 0; turn < 2; turn++ {
		var err error
		// Replayed evidence from a turn predating the completed production must
		// be filtered even when the implementer repeats its exact report identity.
		task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskWriting
			t.Unreachable = []core.Unreachable{original}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		m, _ := a.testMedium(t, p.ID, task.ID)
		if err = m.reset(ctx, task); err != nil {
			t.Fatal(err)
		}
		prod := task.Design[0].Production
		for name, id := range map[string]string{"frame.png": prod.Delivered[0].Attachment, "provenance.json": prod.Provenance} {
			data, err := os.ReadFile(filepath.Join(a.Core.AttachmentsDirectory(task.ID), id))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(m.workspace(task), name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile(filepath.Join(m.workspace(task), "integration.txt"), []byte(fmt.Sprint(turn)), 0600); err != nil {
			t.Fatal(err)
		}
		reply := "Integrated assets.\n```owner-step\n" + `[{"report_id":"frames-report","requirement":"Frames","why":"generator","asset_creation":true}]` + "\n```"
		if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
			t.Fatal(err)
		}
		snap, _ := a.Core.Snapshot(ctx)
		task, _ = snap.FindTask(task.ID)
		if len(task.Revisions) != turn+2 || len(task.Unreachable) != 0 || len(task.Design) != 1 || task.NeedsAssetIntegration() || task.Handoff != nil {
			t.Fatal(task)
		}
		a = restart(t, a)
		snap, _ = a.Core.Snapshot(ctx)
		task, _ = snap.FindTask(task.ID)
		if len(task.Unreachable) != 0 || task.NeedsAssetIntegration() || len(task.Design) != 1 {
			t.Fatal(task)
		}
	}
}

func TestLinkedIDlessClassificationCorrectionGetsExplicitFeedback(t *testing.T) {
	t.Parallel()
	a, _, p, task := ownerAssetFixture(t)
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Design[0].AssetReports[0].ID = ""
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := a.testMedium(t, p.ID, task.ID)
	if err = m.reset(ctx, task); err != nil {
		t.Fatal(err)
	}
	reply := "```owner-step\n" + `[{"requirement":"Frames","why":"generator","asset_creation":false}]` + "\n```"
	if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Failures != 1 || len(got.Notes) == 0 || !strings.Contains(got.Notes[len(got.Notes)-1].Text, "already covered by production") || !got.NeedsAssetIntegration() || len(got.Revisions) != 1 || len(got.Design) != 1 {
		t.Fatal(got)
	}
}

func TestSettledReportDoesNotBlockExplicitProductionRevision(t *testing.T) {
	t.Parallel()
	a, _, p, task := ownerAssetFixture(t)
	ctx := context.Background()
	m, _ := a.testMedium(t, p.ID, task.ID)
	reply := "```production\nframe-01: PNG revised variant\n\nRequirement: frames-report\n```\n```owner-step\n" + `[{"report_id":"frames-report","requirement":"Frames","why":"generator","asset_creation":true}]` + "\n```"
	if err := a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskDesigning || got.Failures != 0 || len(got.Design) != 2 || len(got.Design[1].AssetReports) != 0 || !got.Design[0].IntegrationPending || len(got.Unreachable) != 0 {
		t.Fatal(got)
	}
}
