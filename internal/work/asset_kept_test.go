package work

import (
	"context"
	"fmt"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"strings"
	"testing"
	"time"
)

func TestKeptAssetReportRoutesProductionAndClassification(t *testing.T) {
	t.Parallel()
	for _, production := range []bool{false, true} {
		t.Run(fmt.Sprint(production), func(t *testing.T) {
			t.Parallel()
			a, p, task := draftTeam(t, &scriptedRunner{}, false, "2")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				yes := true
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames", "Icons"}
				t.TeamKept = []string{"Frames"}
				t.Unreachable = []core.Unreachable{{ID: "frames", Source: "review", Finding: "make frames", Criterion: "Frames", Why: "generator", AssetCreation: &yes, Routed: "designer", Revision: 1}, {ID: "icons", Criterion: "Icons", Why: "generator", AssetCreation: &yes, Routed: "designer", Revision: 1}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			reply := "```owner-step\n" + `[{"report_id":"frames","requirement":"Frames","why":"code after all","asset_creation":false},{"report_id":"icons","requirement":"Icons","why":"code","asset_creation":false}]` + "\n```"
			if production {
				reply = "```production\nframe: PNG\n\nRequirement: frames\n```"
			}
			if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if production {
				if got.Status != core.TaskDesigning || len(got.Design) != 1 || len(got.Design[0].AssetReports) != 1 || got.Design[0].AssetReports[0].ID != "frames" || got.Design[0].AssetReports[0].Finding != "make frames" || len(got.Unreachable) != 1 || got.Unreachable[0].ID != "icons" {
					t.Fatal(got)
				}
				a = restart(t, a)
				snap, _ = a.Core.Snapshot(ctx)
				got, _ = snap.FindTask(task.ID)
				if got.Design[0].AssetReports[0].ID != "frames" {
					t.Fatal(got)
				}
			} else if got.Status != core.TaskDeciding || len(got.Unreachable) != 2 || *got.Unreachable[0].AssetCreation {
				t.Fatal(got)
			}
		})
	}
}

func TestLinkedReportsCannotReopenFromOldHandoffOrCorrection(t *testing.T) {
	t.Parallel()
	yes := true
	u := core.Unreachable{ID: "frames", Criterion: "Frames", AssetCreation: &yes, Routed: "designer"}
	task := core.Task{Design: []core.DesignRequest{{ID: "design", AnsweredAt: time.Now().UTC(), Production: &core.Production{}, AssetReports: []core.Unreachable{u}}}}
	got := task.WithoutLinkedAssetReports([]core.Unreachable{u, {ID: "new-finding", Criterion: "Frames", AssetCreation: &yes}})
	if len(got) != 1 || got[0].ID != "new-finding" {
		t.Fatal(got)
	}
	p := core.Project{}
	applyHandoff(&task, &p, core.Handoff{Revision: core.Revision{N: 1}, Unreachable: []core.Unreachable{u}})
	if len(task.Unreachable) != 0 {
		t.Fatal(task)
	}
	no := false
	u.AssetCreation = &no
	applyHandoff(&task, &p, core.Handoff{Revision: core.Revision{N: 2}, Unreachable: []core.Unreachable{u}})
	if len(task.Unreachable) != 0 || len(task.WakeErrors) != 1 || !strings.Contains(task.WakeErrors[0], "already covered by production") {
		t.Fatal(task)
	}
}

func TestUnfinishedHandOffReportsRemainRecoverable(t *testing.T) {
	t.Parallel()
	u := core.Unreachable{ID: "frames", Criterion: "Frames"}
	task := core.Task{Design: []core.DesignRequest{{AnsweredAt: time.Now().UTC(), IntegrationPending: true, Production: &core.Production{Assets: []core.WantedAsset{{Name: "frame", Want: "PNG"}}}, AssetReports: []core.Unreachable{u}}}}
	if got := task.WithoutLinkedAssetReports([]core.Unreachable{u}); len(got) != 1 {
		t.Fatal(got)
	}
	if task.LinkedAssetReport(u) != nil {
		t.Fatal("unfinished production must not suppress classification")
	}
}

func TestKeptAssetReportIdentitiesStayDistinct(t *testing.T) {
	t.Parallel()
	yes := true
	task := core.Task{Criteria: []string{"Frames"}, TeamKept: []string{"Frames"}, Unreachable: []core.Unreachable{
		{ID: "frames-one", Source: "review", Finding: "first obstacle", Criterion: "Frames", AssetCreation: &yes, Routed: "designer"},
		{ID: "frames-two", Source: "review", Finding: "second obstacle", Criterion: "Frames", AssetCreation: &yes, Routed: "designer"},
	}}
	block := `[{"report_id":"frames-one","requirement":"Frames","why":"generator","asset_creation":true},{"report_id":"frames-two","requirement":"Frames","why":"generator","asset_creation":true}]`
	got := routedWriterReports(core.Project{}, task, block)
	if len(got) != 2 || got[0].ID != "frames-one" || got[0].Finding != "first obstacle" || got[1].ID != "frames-two" || got[1].Finding != "second obstacle" {
		t.Fatal(got)
	}
}

func TestLaterDraftKeepsNewObstacleAfterIDlessProduction(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"code", "asset", "kept"} {
		asset := mode == "asset"
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, p, task := draftTeam(t, &scriptedRunner{}, false, "2")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames"}
				if mode == "kept" {
					t.TeamKept = []string{"Frames"}
				}
				t.Unreachable = nil
				t.Design = []core.DesignRequest{{ID: "done", AnsweredAt: time.Now().UTC(), Production: &core.Production{}, AssetReports: []core.Unreachable{{Criterion: "Frames", Why: "old obstacle", Revision: 1}}}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			reply := fmt.Sprintf("New draft\n```owner-step\n[{\"requirement\":\"Frames\",\"why\":\"new obstacle\",\"asset_creation\":%t}]\n```", asset)
			if asset {
				reply += "\n```production\nframe: PNG\n\nRequirement: Frames\n```"
			}
			if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: reply}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if asset {
				if len(got.Design) != 2 || len(got.Design[1].AssetReports) != 1 {
					t.Fatal(got)
				}
				return
			}
			if mode == "kept" {
				if len(got.Unreachable) != 0 || len(got.Design) != 1 || len(got.Revisions) != 2 {
					t.Fatal(got)
				}
				return
			}
			if len(got.Unreachable) != 1 || got.Unreachable[0].Why != "new obstacle" || *got.Unreachable[0].AssetCreation != asset {
				t.Fatal(got)
			}
			if len(got.Design) != 1 {
				t.Fatal(got.Design)
			}
		})
	}
}

func TestKeptUnroutedReportsNeedProductionCoverage(t *testing.T) {
	t.Parallel()
	task := core.Task{Criteria: []string{"Frames"}, TeamKept: []string{"Frames"}}
	block := `[{"requirement":"Frames","why":"generator","asset_creation":true}]`
	if got := routedWriterReports(core.Project{}, task, block); len(got) != 0 {
		t.Fatal(got)
	}
	production := writerReply{assets: []core.WantedAsset{{Name: "frame", Want: "PNG"}}}
	if got := routedWriterReports(core.Project{}, task, block, production); len(got) != 1 {
		t.Fatal(got)
	}
	production.requirements = []string{"Icons"}
	if got := routedWriterReports(core.Project{}, task, block, production); len(got) != 0 {
		t.Fatal(got)
	}
}
