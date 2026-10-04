//go:build !windows

package work

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

func TestRoutedAssetHandBackIsCopiedIntoTheRecordedCommit(t *testing.T) {
	a, runner, p, task := codeTask(t, pass, pass)
	seatDesignerOn(t, a, p.ID, "codex")
	runner.ending = func(n int) string {
		switch n {
		case 1:
			return "\n```owner-step\n[{\"requirement\":\"Generate animation frames\",\"why\":\"generation unavailable in implementer sandbox\",\"asset_creation\":true}]\n```"
		case 2:
			return "\n" + productionBlock(2)
		}
		return ""
	}
	runner.designs = []string{productionJSON(1, 2)}
	runner.onDesigner = func(spec roles.Spec) error {
		attachProductionGroup(t, spec, 1, 2, true)
		return nil
	}
	runner.onEdit = func(dir string, n int) bool {
		if n < 3 {
			return false
		}
		snap, err := a.Core.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		current, _ := snap.FindTask(task.ID)
		prod := current.Design[0].Production
		if len(prod.Delivered) != 2 || prod.Provenance == "" || prod.Archive == "" {
			t.Fatal(prod)
		}
		if err := os.MkdirAll(filepath.Join(dir, "assets"), 0700); err != nil {
			t.Fatal(err)
		}
		copyFile := func(id, name string) {
			data, err := os.ReadFile(filepath.Join(a.Core.AttachmentsDirectory(task.ID), id))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "assets", name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, d := range prod.Delivered {
			copyFile(d.Attachment, d.Asset+".png")
		}
		copyFile(prod.Provenance, "provenance.json")
		return true
	}
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	if len(task.Design) != 1 || len(task.Unreachable) != 0 || len(task.Design[0].Requirements) != 1 || task.DecisionID != "" {
		t.Fatal(task)
	}
	m, _ := a.testMedium(t, p.ID, task.ID)
	ref := task.Revisions[0].Ref
	for _, name := range []string{"frame-01.png", "frame-02.png", "provenance.json"} {
		if data := ownerGit(t, m.workspace(task), "show", ref+":assets/"+name); data == "" {
			t.Fatal("missing committed asset", name)
		}
	}
	data := ownerGit(t, m.workspace(task), "show", ref+":assets/provenance.json")
	if !strings.Contains(data, "synthetic Codex generator") || !strings.Contains(data, "sha256") {
		t.Fatal(data)
	}
}

func TestGitClassificationOnlyKeepsTheExistingDraftForDecision(t *testing.T) {
	for _, pr := range []bool{false, true} {
		t.Run(map[bool]string{false: "without PR", true: "open PR"}[pr], func(t *testing.T) {
			a, _, p, task := codeTask(t, pass, pass)
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			var err error
			task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Unreachable = []core.Unreachable{{Criterion: "Decode images", Why: "can't test", Revision: 1}}
				if pr {
					t.Proposal = &core.Proposal{Number: 12}
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			task, err = a.Core.RouteAsset(ctx, task, task.Unreachable[0], "PM")
			if err != nil {
				t.Fatal(err)
			}
			m, _ := a.testMedium(t, p.ID, task.ID)
			if err = m.reset(ctx, task); err != nil {
				t.Fatal(err)
			}
			writer := task.RolesOf(core.RoleImplementer)[0]
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```owner-step\n[{\"requirement\":\"Decode images\",\"why\":\"needs a live owner check\",\"asset_creation\":false}]\n```", Session: []byte(`{"engine":"claude","id":"corrected"}`)}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if got.Status != core.TaskDeciding || got.Round != task.Round || len(got.Revisions) != 1 || got.Revisions[0].Ref != task.Revisions[0].Ref || len(got.Unreachable) != 1 || got.Unreachable[0].AssetCreation == nil || *got.Unreachable[0].AssetCreation || got.Unreachable[0].Routed != "" || got.Failures != 0 {
				t.Fatal(got)
			}
		})
	}
}

func TestPartialProductionRequiresClassificationAndAssetCommit(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, pr := range []bool{false, true} {
			a, runner, p, task := codeTask(t, pass, pass)
			seatDesignerOn(t, a, p.ID, "codex")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				yes := true
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames", "Decode images"}
				t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "generator unavailable", Revision: 1, AssetCreation: &yes}, {Criterion: "Decode images", Why: "decoder blocked", Revision: 1}}
				if pr {
					t.Proposal = &core.Proposal{Number: 12}
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := a.testMedium(t, p.ID, task.ID)
			writer := task.RolesOf(core.RoleImplementer)[0]
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: PNG\n\nRequirement: Frames\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			runner.designs = []string{productionJSON(1, 1)}
			runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, true); return nil }
			designer, _ := task.Designer()
			if err = a.design(ctx, p, task, m, designer); err != nil {
				t.Fatal(err)
			}
			if restart {
				next := New(a.Core, a.Config, false)
				next.runner, next.meter = runner, a.meter
				a = next
				if err = a.resume(ctx); err != nil {
					t.Fatal(err)
				}
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if !task.NeedsAssetIntegration() || len(task.Unreachable) != 1 || !task.Unreachable[0].NeedsAssetReply() {
				t.Fatal(task)
			}
			// Omitting the uncovered report cannot silently resolve it.
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "Ready"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if len(task.Revisions) != 1 || len(task.Unreachable) != 1 || task.Unreachable[0].AssetCreation != nil {
				t.Fatal(task)
			}
			// Classification is recorded, but never passes the pre-production draft.
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```owner-step\n[{\"requirement\":\"Decode images\",\"why\":\"code, not asset creation\",\"asset_creation\":false}]\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Status != core.TaskWriting || len(task.Revisions) != 1 || !task.NeedsAssetIntegration() || task.Unreachable[0].AssetCreation == nil || *task.Unreachable[0].AssetCreation {
				t.Fatal(task)
			}
			if err = m.reset(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "No change"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Status != core.TaskWriting || task.DecisionID != "" || len(task.Revisions) != 1 {
				t.Fatal(task)
			}
			// Only the new asset/provenance commit releases the integration hold.
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
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "Integrated assets and fixed decoding"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if len(task.Revisions) != 2 || task.NeedsAssetIntegration() || len(task.Unreachable) != 0 || task.Status != core.TaskReviewing {
				t.Fatal(task)
			}
			for _, name := range []string{"frame.png", "provenance.json"} {
				if ownerGit(t, m.workspace(task), "show", task.Revisions[1].Ref+":"+name) == "" {
					t.Fatal("missing committed hand-back", name)
				}
			}
		}
	}
}

func TestPMAssetLandingHoldRoutesBeforeOwnerDecision(t *testing.T) {
	for _, approve := range []bool{false, true} {
		for _, openPR := range []bool{false, true} {
			for _, asset := range []bool{false, true} {
				if approve && !asset {
					continue
				}
				t.Run(fmt.Sprintf("approve=%v/PR=%v/asset=%v", approve, openPR, asset), func(t *testing.T) {
					reply := `{"land":false,"reason":"Wait for another change"}`
					if asset {
						reply = `{"land":false,"reason":"Cannot generate frames here","blocked_asset":{"requirement":"Frames","why":"generator is unavailable to implementer","asset_creation":true}}`
						if openPR {
							reply = `{"land":false,"reason":"Cannot generate frames here","requirement":"Frames","asset_creation":true}`
						}
					}
					if approve && asset {
						reply = strings.Replace(reply, `"land":false`, `"land":true`, 1)
					}
					w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
					w.runner.pmLand = []string{reply}
					a, p := w.a, w.p
					task, err := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Animation", Criteria: []string{"Frames"}})
					if err != nil {
						t.Fatal(err)
					}
					seatDesigner(t, a, p.ID)
					task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
					ctx := context.Background()
					snap, _ := a.Core.Snapshot(ctx)
					p, _ = findProject(snap, p.ID)
					task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Status = core.TaskDeciding
						t.Criteria = []string{"Frames"}
						for _, checker := range t.Checkers() {
							t.Verdicts = append(t.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, TextVersion: t.TextVersion, Outcome: core.VerdictPass})
						}
						if openPR {
							t.Proposal = &core.Proposal{Number: 1, URL: "https://example.invalid/1"}
						}
						return "", nil
					})
					if err != nil {
						t.Fatal(err)
					}
					m, _ := a.mediumFor(ctx, p, task.Playbook)
					if err = a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
						t.Fatal(err)
					}
					snap, _ = a.Core.Snapshot(ctx)
					task, _ = snap.FindTask(task.ID)
					if asset {
						if task.Status != core.TaskWriting || task.DecisionID != "" || task.LandDecision != nil || len(task.Unreachable) != 1 || task.Unreachable[0].Source != "landing" {
							t.Fatal(task, openDecision(t, a, task))
						}
						if err = a.recordDraft(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0].Name, roles.Result{Text: "```production\nframe: PNG\n```"}, 0, nil); err != nil {
							t.Fatal(err)
						}
						snap, _ = a.Core.Snapshot(ctx)
						task, _ = snap.FindTask(task.ID)
						if task.Status != core.TaskDesigning || task.DecisionID != "" || len(task.Unreachable) != 0 {
							t.Fatal(task)
						}
					} else if task.Status != core.TaskWaiting || task.DecisionID == "" {
						t.Fatal("ordinary hold changed", task)
					}
				})
			}
		}
	}
}

func TestMalformedPMLandingAssetReportDoesNotBecomeOwnerHold(t *testing.T) {
	for name, reply := range map[string]string{
		"missing requirement":           `{"land":false,"reason":"Generate frames","asset_creation":true}`,
		"nested string":                 `{"land":false,"reason":"Generate frames","blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":"true"}}`,
		"top string":                    `{"land":false,"reason":"Generate frames","requirement":"Frames","asset_creation":"true"}`,
		"nested missing classification": `{"land":false,"reason":"Generate frames","blocked_asset":{"requirement":"Frames","why":"sandbox"}}`,
		"nested number":                 `{"land":true,"reason":"Ready","blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
			w.runner.pmLand = []string{reply, reply}
			ctx := context.Background()
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Frames"})
			if err != nil {
				t.Fatal(err)
			}
			seatDesigner(t, w.a, w.p.ID)
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			snap, _ := w.a.Core.Snapshot(ctx)
			p, _ := findProject(snap, w.p.ID)
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				for _, checker := range t.Checkers() {
					t.Verdicts = append(t.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := w.a.mediumFor(ctx, p, task.Playbook)
			if err = w.a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
				t.Fatal(err)
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.DecisionID != "" || task.Failures != 1 || len(task.Design) != 0 || task.LandDecision != nil {
				t.Fatal(task)
			}
		})
	}
}

func TestMalformedSiblingWriterReportSurvivesProductionRestart(t *testing.T) {
	original := "```owner-step\n" + `[{"requirement":"Frames","why":"sandbox","asset_creation":true},{"requirement":"Icons","why":"sandbox","asset_creation":"true"}]` + "\n```"
	spec := productionBlock(1)
	spec = strings.Replace(spec, "Match the existing art.", "Requirement: Frames", 1)
	runner := &scriptedRunner{writerReplies: []string{original, spec}, designs: []string{productionJSON(1, 1)}}
	runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, false); return nil }
	a, p, current := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	current = stepUntil(t, a, current.ID, func(t core.Task) bool { return t.Status == core.TaskDesigning })
	if len(current.Unreachable) != 1 || current.Unreachable[0].Criterion != "Icons" || current.Unreachable[0].AssetCreation != nil || !slices.Equal(current.OpenDesign().Requirements, []string{"Frames"}) {
		t.Fatal(current)
	}
	a = restart(t, a)
	current = stepUntil(t, a, current.ID, func(t core.Task) bool {
		return t.Status == core.TaskWriting && len(t.Design) > 0 && !t.Design[0].Open()
	})
	a = restart(t, a)
	snap, _ := a.Core.Snapshot(context.Background())
	current, _ = snap.FindTask(current.ID)
	if len(current.Unreachable) != 1 || !current.Unreachable[0].NeedsAssetReply() {
		t.Fatal(current)
	}
	if err := writerAssetError(current, parseWriterReply("Finished the next draft.", true)); err == nil {
		t.Fatal("unknown Icons report silently omitted")
	}
}

func TestPMLandingCorrectionCannotOmitAssetObstacle(t *testing.T) {
	for name, reply := range map[string]string{
		"nested": `{"land":false,"blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":true}}`,
		"top":    `{"land":false,"requirement":"Frames","asset_creation":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
			w.runner.pmLand = []string{reply, `{"land":true,"reason":"Ready"}`}
			ctx := context.Background()
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Frames"})
			if err != nil {
				t.Fatal(err)
			}
			seatDesigner(t, w.a, w.p.ID)
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			snap, _ := w.a.Core.Snapshot(ctx)
			p, _ := findProject(snap, w.p.ID)
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				for _, checker := range t.Checkers() {
					t.Verdicts = append(t.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := w.a.mediumFor(ctx, p, task.Playbook)
			if err = w.a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
				t.Fatal(err)
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.DecisionID != "" || task.Failures != 1 || len(task.Design) != 0 || task.LandDecision != nil {
				t.Fatal(task)
			}
		})
	}
}

func TestRetiredProductionAllowsUnchangedPR(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, pr := range []bool{true} {
			a, runner, p, task := codeTask(t, pass, pass)
			seatDesignerOn(t, a, p.ID, "codex")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				yes := true
				t.Status = core.TaskWriting
				t.Criteria = []string{"Frames", "Decode images"}
				t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "generator unavailable", Revision: 1, AssetCreation: &yes}, {Criterion: "Decode images", Why: "decoder blocked", Revision: 1}}
				if pr {
					t.Proposal = &core.Proposal{Number: 12}
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := a.testMedium(t, p.ID, task.ID)
			writer := task.RolesOf(core.RoleImplementer)[0]
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: PNG\n\nRequirement: Frames\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			runner.designs = []string{productionJSON(1, 1)}
			runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, true); return nil }
			designer, _ := task.Designer()
			if err = a.design(ctx, p, task, m, designer); err != nil {
				t.Fatal(err)
			}
			if restart {
				next := New(a.Core, a.Config, false)
				next.runner, next.meter = runner, a.meter
				a = next
				if err = a.resume(ctx); err != nil {
					t.Fatal(err)
				}
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if !task.NeedsAssetIntegration() || len(task.Unreachable) != 1 || !task.Unreachable[0].NeedsAssetReply() {
				t.Fatal(task)
			}

			_, err = a.Core.EditTask(ctx, core.EditInput{Project: p.ID, Task: task.ID, Kind: core.RolePM, By: "Owner", Criteria: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Core.UpdateBrief(ctx, p.ID, core.BriefInput{Goal: p.Brief.Goal})
			if err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if restart {
				next := New(a.Core, a.Config, false)
				next.runner, next.meter = runner, a.meter
				a = next
				if err = a.resume(ctx); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				task, _ = snap.FindTask(task.ID)
			}
			if task.NeedsAssetIntegration() {
				t.Fatal(task)
			}
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "Ready"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Failures != 0 || len(task.Revisions) != 1 || task.Status != core.TaskLanding {
				t.Fatal(task)
			}
		}
	}
}

func TestAmbiguousCoverageCorrectionRetainsUncoveredAssets(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, pr := range []bool{false, true} {
			a, runner, p, task := codeTask(t, pass, pass)
			seatDesignerOn(t, a, p.ID, "codex")
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				yes := true
				t.Status = core.TaskWriting
				t.Criteria = []string{"Blue frames", "Red frames"}
				t.Unreachable = []core.Unreachable{{Criterion: "Blue frames", Why: "generator unavailable", Revision: 1, AssetCreation: &yes}, {Criterion: "Red frames", Why: "generator unavailable", Revision: 1, AssetCreation: &yes}}
				if pr {
					t.Proposal = &core.Proposal{Number: 12}
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := a.testMedium(t, p.ID, task.ID)
			writer := task.RolesOf(core.RoleImplementer)[0]
			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: blue PNG\n\nRequirement: Frames\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snapBefore, _ := a.Core.Snapshot(ctx)
			task, _ = snapBefore.FindTask(task.ID)
			if len(task.Design) != 0 || len(task.Unreachable) != 2 {
				t.Fatal(task)
			}

			if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: PNG\n\nRequirement: Blue frames\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			runner.designs = []string{productionJSON(1, 1)}
			runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, true); return nil }
			designer, _ := task.Designer()
			if err = a.design(ctx, p, task, m, designer); err != nil {
				t.Fatal(err)
			}
			if restart {
				next := New(a.Core, a.Config, false)
				next.runner, next.meter = runner, a.meter
				a = next
				if err = a.resume(ctx); err != nil {
					t.Fatal(err)
				}
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if !task.NeedsAssetIntegration() || len(task.Unreachable) != 1 || !task.Unreachable[0].NeedsAssetReply() {
				t.Fatal(task)
			}

			if len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "Red frames" || task.Unreachable[0].AssetCreation == nil || !*task.Unreachable[0].AssetCreation {
				t.Fatal(task)
			}
		}
	}
}

func TestPMLandingCorrectionCannotNullAssetObstacle(t *testing.T) {
	for name, reply := range map[string]string{
		"nested": `{"land":false,"blocked_asset":{"requirement":"Frames","why":"sandbox","asset_creation":true}}`,
		"top":    `{"land":false,"requirement":"Frames","asset_creation":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newPMPush(t, core.ApprovePM, "2", passes(4)...)
			w.runner.pmLand = []string{reply, `{"land":false,"reason":"Ready","blocked_asset":null}`}
			ctx := context.Background()
			task, err := w.a.Core.QueueTask(ctx, w.p.ID, core.TaskInput{Objective: "Frames"})
			if err != nil {
				t.Fatal(err)
			}
			seatDesigner(t, w.a, w.p.ID)
			task = stepUntil(t, w.a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			snap, _ := w.a.Core.Snapshot(ctx)
			p, _ := findProject(snap, w.p.ID)
			task, err = w.a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				for _, checker := range t.Checkers() {
					t.Verdicts = append(t.Verdicts, core.Verdict{Role: checker.Name, Revision: 1, BriefVersion: p.Brief.Version, Outcome: core.VerdictPass})
				}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			m, _ := w.a.mediumFor(ctx, p, task.Playbook)
			if err = w.a.pmLanding(ctx, p, task, task.Revisions[0], m); err != nil {
				t.Fatal(err)
			}
			snap, _ = w.a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.DecisionID != "" || task.Failures != 1 || len(task.Design) != 0 || task.LandDecision != nil {
				t.Fatal(task)
			}
		})
	}
}
