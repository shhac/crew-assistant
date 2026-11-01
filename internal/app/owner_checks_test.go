package app

import (
	"context"
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
	"time"
)

func TestAssistantOwnerChecks(t *testing.T) {
	a, p, first, second := blockerFixture(t, false)
	ctx := context.Background()
	for i, task := range []core.Task{first, second} {
		_, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskLanded
			t.OwnerChecks = []string{"Live check", "Tag release"}
			t.LandedAt = time.Date(2020, 1, i+1, 0, 0, 0, 0, time.UTC)
			return "Landed", nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	out, err := blockerCall(t, a, "list_owner_checks", map[string]string{"project_id": ""})
	if err != nil {
		t.Fatal(err)
	}
	list := out.([]core.OutstandingCheck)
	if len(list) != 4 || list[0].TaskID != first.ID || list[2].TaskID != second.ID {
		t.Fatal(list)
	}
	for _, status := range []string{"done", "skipped", "outstanding"} {
		out, err := blockerCall(t, a, "mark_owner_check", map[string]string{"project_id": p.ID, "task_id": first.Ref, "step": "Live check", "status": status, "note": "owner asked"})
		if err != nil {
			t.Fatal(err)
		}
		task := out.(core.Task)
		m := task.Checklist[0].Mark
		if m.By != core.FromAssistant || m.At.IsZero() || m.Status != status {
			t.Fatal(m)
		}
	}
	out, err = blockerCall(t, a, "list_owner_checks", map[string]string{"project_id": "other"})
	if err != nil || len(out.([]core.OutstandingCheck)) != 0 {
		t.Fatal(out, err)
	}
}
