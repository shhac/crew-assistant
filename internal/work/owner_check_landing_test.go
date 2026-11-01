package work

import (
	"github.com/shhac/crew-assistant/internal/core"
	"testing"
	"time"
)

func TestLandingRecordsOwnerCheckTime(t *testing.T) {
	for _, way := range []string{core.LandBranch, core.LandPush} {
		task := core.Task{OwnerChecks: []string{"Live check"}, Playbook: &core.Playbook{Land: core.LandPolicy{Via: way}}}
		p := core.Project{}
		said := landedOn(&task, &p, core.Revision{}, "main", "")
		if task.LandedAt.IsZero() || said == "" {
			t.Fatal(task, said)
		}
		want := core.TaskDelivered
		if way == core.LandPush {
			want = core.TaskLanded
		}
		if task.Status != want {
			t.Fatal(task.Status)
		}
	}
}

func TestLandingAgainPreservesOwnerCheckTime(t *testing.T) {
	first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	task := core.Task{LandedAt: first, Status: core.TaskDelivered, Playbook: &core.Playbook{Land: core.LandPolicy{Via: core.LandPush}}}
	p := core.Project{}
	landedWords(&task, &p, core.Revision{Ref: "commit"}, "main", "")
	if !task.LandedAt.Equal(first) || p.Landed == nil || !p.Landed.At.After(first) {
		t.Fatalf("first delivery time and latest project landing: %+v %+v", task, p.Landed)
	}
}
