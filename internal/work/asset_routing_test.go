package work

import (
	"context"
	"fmt"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"slices"
	"strings"
	"testing"
)

func TestMixedMalformedPMFindingsRetainConfirmedAssets(t *testing.T) {
	t.Parallel()
	reply := `{"findings":[{"finding":1,"asset_creation":true},{"finding":2,"asset_creation":"false"}]}`
	runner := &scriptedRunner{escalate: []string{reply, reply}}
	a, p, task := draftTeam(t, runner, true, "1")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	findings := []core.Finding{{Criterion: "Frames", Note: "Generate frames"}, {Criterion: "Icons", Note: "Generate icons"}}
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDeciding
		no := false
		for _, f := range findings {
			t.Unreachable = append(t.Unreachable, reviewReport(p, *t, finding{Role: "Reviewer", Criterion: f.Criterion, Note: f.Note}, &no))
		}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.escalate(ctx, p, task, nil, []core.Verdict{{Role: "Reviewer", Findings: findings}}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskWriting || got.DecisionID != "" || !slices.ContainsFunc(got.Unreachable, func(u core.Unreachable) bool {
		return u.Criterion == "Frames" && u.AssetCreation != nil && *u.AssetCreation && u.NeedsAssetReply()
	}) {
		t.Fatal(got)
	}
}

func TestProductionLinksNormalizedQuotes(t *testing.T) {
	t.Parallel()
	for _, quote := range []string{"Frames", strings.Repeat("x", 510)} {
		criterion := "Generate Frames"
		if len(quote) > 500 {
			criterion = quote
		}
		in := parseWriterReply("```production\nframe: PNG\n\nRequirement: "+quote+"\n```", true)
		in.unmet = fmt.Sprintf(`[{"requirement":%q,"why":"sandbox","asset_creation":true}]`, quote)
		reports, _ := parseOwnerSteps(in.unmet, 1, []string{criterion}, nil, nil)
		if err := writerAssetError(core.Task{Roles: []core.Role{{Name: "D", Kinds: []string{core.RoleDesigner}}}}, in); err != nil {
			t.Fatal(err)
		}
		if links := productionLinks(in, reports); len(links) != 1 || links[0] != criterion {
			t.Fatal(links, reports)
		}
	}
}

func TestFirstDraftRetainsRestoredAssetWithoutDesigner(t *testing.T) {
	t.Parallel()
	a, _, current := draftTeam(t, &scriptedRunner{reviews: []string{pass}}, false, "2")
	current, err := a.Core.UpdateTask(context.Background(), current.ID, func(t *core.Task, _ *core.Project) (string, error) {
		yes := true
		t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "The current team has no designer", AssetCreation: &yes}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current = stepUntil(t, a, current.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	if len(current.Unreachable) != 1 || current.Unreachable[0].Criterion != "Frames" || current.Unreachable[0].Revision != 1 {
		t.Fatal(current)
	}
	current = stepUntil(t, a, current.ID, func(t core.Task) bool { return t.DecisionID != "" })
	if current.Status != core.TaskWaiting {
		t.Fatal(current)
	}
}

func TestProductionCorrectionRetainsUncoveredUnknownReport(t *testing.T) {
	t.Parallel()
	task := core.Task{Roles: []core.Role{{Name: "Designer", Kinds: []string{core.RoleDesigner}}}}
	previous := `[{"requirement":"Frames","why":"sandbox","asset_creation":true},{"requirement":"Decode images","why":"decoder blocked"}]`
	in := parseWriterReply("```production\nframe: PNG\n\nRequirement: Frames\n```", true)
	block, err := mergeWriterReports(task, previous, in)
	if err != nil {
		t.Fatal(err)
	}
	in.unmet = block
	if err = writerAssetError(task, in); err != nil {
		t.Fatal(err)
	}
	entries, _ := ownerStepEntries(block)
	if len(entries) != 2 || entries[1].AssetCreation != nil {
		t.Fatal(block)
	}
	// Feed the normalized correction through the real atomic hand-off.
	runner := &scriptedRunner{writerReplies: []string{"```owner-step\n" + previous + "\n```", "```production\nframe: PNG\n\nRequirement: Frames\n```"}}
	a, p, actual := draftTeam(t, runner, false, "2")
	seatDesigner(t, a, p.ID)
	actual = stepUntil(t, a, actual.ID, func(t core.Task) bool { return t.Status == core.TaskDesigning })
	if len(actual.Unreachable) != 1 || actual.Unreachable[0].Criterion != "Decode images" || actual.Unreachable[0].AssetCreation != nil || !actual.Unreachable[0].NeedsAssetReply() || !slices.Equal(actual.OpenDesign().Requirements, []string{"Frames"}) {
		t.Fatal(actual)
	}
	// Without explicit coverage, Frames and decoding are ambiguous.
	in = parseWriterReply("```production\nframe: PNG\n```", true)
	in.unmet, err = mergeWriterReports(task, previous, in)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ = ownerStepEntries(in.unmet)
	if entries[1].AssetCreation != nil || writerAssetError(task, in) == nil {
		t.Fatal(in)
	}
}

func TestProductionWithMultipleDurableRoutesRequiresCoverage(t *testing.T) {
	t.Parallel()
	for _, classified := range []bool{false, true} {
		runner := &scriptedRunner{}
		a, p, task := draftTeam(t, runner, false, "2")
		seatDesigner(t, a, p.ID)
		task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
		runner.writerReplies = []string{"```production\nframe: PNG\n```", "```production\nframe: PNG\n\nRequirement: Frames\n```"}
		yes := true
		var classification *bool
		if classified {
			classification = &yes
		}
		var err error
		task, err = a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskWriting
			t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "sandbox", Revision: 1, AssetCreation: classification, Routed: "PM: production required"}, {Criterion: "Icons", Why: "sandbox", Revision: 1, AssetCreation: classification, Routed: "PM: production required"}}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		in := parseWriterReply(runner.writerReplies[0], true)
		in.unmet, err = mergeWriterReports(task, "", in)
		if err == nil && writerAssetError(task, in) == nil {
			t.Fatal("ambiguous durable routes accepted", in, err)
		}
		// The correction must produce one linked request, never an unlinked
		// request followed by a second request for the same frame.
		m, _ := a.mediumFor(context.Background(), p, task.Playbook)
		if err = a.write(context.Background(), p, task, m, task.RolesOf(core.RoleImplementer)[0]); err != nil {
			t.Fatal(err)
		}
		snap, _ := a.Core.Snapshot(context.Background())
		task, _ = snap.FindTask(task.ID)
		if task.Status != core.TaskDesigning || len(task.Design) != 1 || !slices.Equal(task.OpenDesign().Requirements, []string{"Frames"}) || len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "Icons" || task.DecisionID != "" {
			t.Fatal(task)
		}
		if len(task.OpenDesign().AssetReports) != 1 || task.OpenDesign().AssetReports[0].Criterion != "Frames" {
			t.Fatal(task.OpenDesign())
		}

	}
}

func TestProductionCoverageMustResolvePendingReports(t *testing.T) {
	t.Parallel()
	yes := true
	corrected := core.Task{Roles: []core.Role{{Name: "Designer", Kinds: []string{core.RoleDesigner}}}, Unreachable: []core.Unreachable{{Criterion: "Frames", AssetCreation: &yes}, {Criterion: "Decode", Routed: "classification required"}}}
	entries := writerReply{assets: []core.WantedAsset{{Name: "frame", Want: "PNG"}}, requirements: []string{"Decode"}, unmet: `[{"requirement":"Decode","why":"code only","asset_creation":false}]`}
	if writerAssetError(corrected, entries) == nil {
		t.Fatal("code correction falsely covered by production")
	}
	for _, coverage := range []string{"", "Unreported artwork"} {
		runner := &scriptedRunner{}
		a, p, task := draftTeam(t, runner, false, "2")
		seatDesigner(t, a, p.ID)
		task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
		task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			yes := true
			t.Status = core.TaskWriting
			t.Unreachable = []core.Unreachable{{Criterion: "Frames", Revision: 1, AssetCreation: &yes}, {Criterion: "Icons", Revision: 1, AssetCreation: &yes}}
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		bad := "```production\nframe: PNG\n\nRequirement: " + coverage + "\n```"
		in := parseWriterReply(bad, true)
		in.unmet, err = mergeWriterReports(task, "", in)
		if err == nil && writerAssetError(task, in) == nil {
			t.Fatal("invalid coverage accepted", coverage, err)
		}
		runner.writerReplies = []string{bad, "```production\nframe: PNG\n\nRequirement: Frames\n```"}
		m, _ := a.mediumFor(context.Background(), p, task.Playbook)
		if err = a.write(context.Background(), p, task, m, task.RolesOf(core.RoleImplementer)[0]); err != nil {
			t.Fatal(err)
		}
		snap, _ := a.Core.Snapshot(context.Background())
		got, _ := snap.FindTask(task.ID)
		if len(got.Design) != 1 || len(got.Design[0].AssetReports) != 1 || got.Design[0].AssetReports[0].Criterion != "Frames" || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Icons" {
			t.Fatal(got)
		}
	}
}

func TestFailedRoundLimitCorrectionPreservesExplicitAssetFinding(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{escalate: []string{`{"findings":[{"finding":1,"asset_creation":true}]}`, "invalid"}}
	a, p, task := draftTeam(t, runner, true, "1")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Animation", Note: "Generate frames"}}}}
	f := remaining(changes)[0]
	task, err := a.Core.UpdateTask(context.Background(), task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		no := false
		t.Status = core.TaskDeciding
		t.Unreachable = []core.Unreachable{reviewReport(p, *t, f, &no)}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.escalate(context.Background(), p, task, nil, changes); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskWriting || got.DecisionID != "" || len(got.Unreachable) != 1 || got.Unreachable[0].AssetCreation == nil || !*got.Unreachable[0].AssetCreation {
		t.Fatal(got)
	}
}

func TestPreparedHandoffCannotRestoreRemovedLegacyBriefReport(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, task := draftTeam(t, runner, false, "2")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	if _, err := a.Core.UpdateBrief(ctx, p.ID, core.BriefInput{Goal: "Assets", Criteria: []string{"Brief frames"}}); err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Handoff = &core.Handoff{Name: "prepared", Revision: core.Revision{N: 2, Ref: "prepared-ref"}, Unreachable: []core.Unreachable{{Criterion: "Brief frames", Why: "generator unavailable", Revision: 2}, {Criterion: "Timing", Why: "code", Revision: 2}}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateBrief(ctx, p.ID, core.BriefInput{Goal: "Assets", Criteria: []string{"Replacement"}}); err != nil {
		t.Fatal(err)
	}
	restart := New(a.Core, a.Config, false)
	if err = restart.commitHandoff(ctx, task.ID, "prepared"); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Handoff != nil || len(got.Revisions) != 2 || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Timing" || got.Status != core.TaskReviewing {
		t.Fatal(got)
	}
}

func TestDesignerDisappearanceAfterProductionRequestRestoresLinkedRequirement(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, task := draftTeam(t, runner, false, "2")
	seatDesignerOn(t, a, p.ID, "codex")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		yes := true
		t.Status = core.TaskWriting
		t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "generator unavailable", Revision: 1, AssetCreation: &yes}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := a.mediumFor(ctx, p, task.Playbook)
	writer := task.RolesOf(core.RoleImplementer)[0]
	if err = a.recordDraft(ctx, p, task, m, writer.Name, roles.Result{Text: "```production\nframe-01: PNG\nframe-02: PNG\n\nRequirement: Frames\n```"}, 0, nil); err != nil {
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
	snap, _ = a.Core.Snapshot(ctx)
	p, _ = findProject(snap, p.ID)
	pb := *p.Playbook
	pb.Roles = slices.DeleteFunc(slices.Clone(pb.Roles), func(r core.Role) bool { return r.Holds(core.RoleDesigner) })
	if _, err = a.Core.SetPlaybook(ctx, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWaiting; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UseProjectTeam(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskDesigning; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	restart := New(a.Core, a.Config, false)
	restart.runner, restart.meter = runner, a.meter
	if err = restart.design(ctx, p, task, m, core.Role{}); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskDeciding || len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Frames" || !strings.Contains(got.Unreachable[0].Why, "no designer") || len(got.Design[0].Production.Delivered) != 1 || got.Design[0].Production.Provenance == "" || got.Design[0].Production.Archive == "" || len(got.Revisions) != 1 {
		t.Fatal(got)
	}
}

func TestProductionCorrectionCoversOnlyNamedAssetReport(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{writerReplies: []string{
		"```owner-step\n[{\"requirement\":\"Frames\",\"why\":\"generator unavailable\",\"asset_creation\":true},{\"requirement\":\"Icons\",\"why\":\"generator unavailable\",\"asset_creation\":true}]\n```",
		"```production\nframe: PNG 32x32\n\nRequirement: Frames\n```",
	}}
	a, p, task := draftTeam(t, runner, false, "2")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return t.Status == core.TaskDesigning })
	if !slices.Equal(task.OpenDesign().Requirements, []string{"Frames"}) || len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "Icons" || task.DecisionID != "" {
		t.Fatal(task)
	}
	// After the partial hand-back, the remaining creation obstacle must not
	// disappear just because the next reply omits the original report block.
	if writerAssetError(task, writerReply{}) == nil {
		t.Fatal("unfinished Icons allowed another draft")
	}
	in := parseWriterReply("```production\nicon: PNG 32x32\n```", true)
	block, err := mergeWriterReports(task, "", in)
	if err != nil {
		t.Fatal(err)
	}
	reports, _ := parseOwnerSteps(block, 0, nil, nil, nil)
	if !slices.Equal(productionLinks(in, reports), []string{"Icons"}) {
		t.Fatal(reports)
	}
}

func TestReviewFindingsSharingCriterionAndRecurringDrafts(t *testing.T) {
	t.Parallel()
	for _, pm := range []bool{false, true} {
		t.Run(fmt.Sprint("PM=", pm), func(t *testing.T) {
			a, p, task := draftTeam(t, &scriptedRunner{}, pm, "1")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Criteria = []string{"Animation"}
				return "", nil
			})
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Animation", Note: "Timing is wrong"}, {Criterion: "Animation", Note: "Generate every frame"}}}}
			for round := 1; round <= 2; round++ {
				if err = a.escalate(ctx, p, task, nil, changes); err != nil {
					t.Fatal(err)
				}
				snap, _ := a.Core.Snapshot(ctx)
				task, _ = snap.FindTask(task.ID)
				if task.Status != core.TaskWriting {
					t.Fatal("first finding did not route", task)
				}
				u := task.Unreachable[len(task.Unreachable)-1]
				correction := fmt.Sprintf("```owner-step\n[{\"report_id\":%q,\"requirement\":\"Animation\",\"why\":\"timing is code\",\"asset_creation\":false}]\n```", u.ID)
				if err = a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: correction}, 0, nil); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				task, _ = snap.FindTask(task.ID)
				if err = a.escalate(ctx, p, task, nil, changes); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				task, _ = snap.FindTask(task.ID)
				if task.Status != core.TaskWriting || task.Unreachable[len(task.Unreachable)-1].Finding != "Generate every frame" {
					t.Fatal("second finding did not route", task)
				}
				if err = a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: "```production\nframe: PNG\n```"}, 0, nil); err != nil {
					t.Fatal(err)
				}
				snap, _ = a.Core.Snapshot(ctx)
				task, _ = snap.FindTask(task.ID)
				if task.Status != core.TaskDesigning || len(task.OpenDesign().Requirements) != 1 || len(task.Unreachable) != round {
					t.Fatal(task)
				}
				request := task.OpenDesign()
				if !slices.Equal(request.Requirements, []string{"Animation"}) || len(request.AssetReports) != 1 || request.AssetReports[0].Finding != "Generate every frame" || request.AssetReports[0].ID == "" {
					t.Fatal(request)
				}
				if round == 1 {
					task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
						t.Status = core.TaskDeciding
						t.Revisions = append(t.Revisions, core.Revision{N: 2, Ref: "next"})
						return "", nil
					})
				}
			}
		})
	}
}

func TestCriterionFreeReviewCorrectionKeepsNormalizedIdentity(t *testing.T) {
	t.Parallel()
	for _, note := range []string{"Animation needs timing fixed", strings.Repeat("Long unmatched note. ", 40)} {
		a, p, task := draftTeam(t, &scriptedRunner{}, false, "1")
		seatDesigner(t, a, p.ID)
		task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
		ctx := context.Background()
		task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
			t.Status = core.TaskDeciding
			t.Criteria = []string{"Animation"}
			return "", nil
		})
		changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Note: note}}}}
		if err := a.escalate(ctx, p, task, nil, changes); err != nil {
			t.Fatal(err)
		}
		snap, _ := a.Core.Snapshot(ctx)
		task, _ = snap.FindTask(task.ID)
		m, _ := a.mediumFor(ctx, p, task.Playbook)
		if err := a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: "```production\nframe: PNG\n```"}, 0, nil); err != nil {
			t.Fatal(err)
		}
		snap, _ = a.Core.Snapshot(ctx)
		task, _ = snap.FindTask(task.ID)
		if len(task.Unreachable) != 0 || task.Status != core.TaskDesigning || len(task.OpenDesign().Requirements) != 1 {
			t.Fatal(task)
		}
	}
}

func TestCompletedRewriteDropsOmittedOrdinaryCodeReports(t *testing.T) {
	t.Parallel()
	a, p, task := draftTeam(t, &scriptedRunner{}, false, "2")
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		no := false
		t.Status = core.TaskWriting
		t.Unreachable = []core.Unreachable{{Criterion: "Timing", Why: "old obstacle", Revision: 1, AssetCreation: &no}}
		return "", nil
	})
	m, _ := a.mediumFor(ctx, p, task.Playbook)
	if err := a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: "Fixed timing in the new draft."}, 0, nil); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	if len(task.Unreachable) != 0 || len(task.Revisions) != 2 || task.DecisionID != "" {
		t.Fatal(task)
	}
}

func TestSingletonOwnerStepWithoutDesignerKeepsExistingContract(t *testing.T) {
	t.Parallel()
	for _, pm := range []bool{false, true} {
		t.Run(fmt.Sprint("PM=", pm), func(t *testing.T) {
			runner := &scriptedRunner{reviews: []string{pass}, writerText: "Done.\n```owner-step\n{\"requirement\":\"Run on owner machine\",\"why\":\"requires owner machine\"}\n```", ownerStep: []string{`{"owner_step":true,"step":"Run on owner machine","reason":"Only the owner can do this"}`}}
			a, _, task := draftTeam(t, runner, pm, "2")
			task = stepUntil(t, a, task.ID, waiting)
			d := openDecision(t, a, task)
			if d.OwnerStep == nil || task.Failures != 0 || len(task.Design) != 0 || runner.writes != 1 {
				t.Fatal(task, d, runner.writes)
			}
		})
	}
}

func TestUnclassifiedReviewAfterTeamChangeUsesRoundLimitDecision(t *testing.T) {
	t.Parallel()
	a, p, task := draftTeam(t, &scriptedRunner{}, false, "1")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskDeciding; return "", nil })
	changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Note: "Change animation timing"}}}}
	if err := a.escalate(ctx, p, task, nil, changes); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	p, _ = findProject(snap, p.ID)
	pb := *p.Playbook
	pb.Roles = slices.DeleteFunc(slices.Clone(pb.Roles), func(r core.Role) bool { return r.Holds(core.RoleDesigner) })
	if _, err := a.Core.SetPlaybook(ctx, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWaiting; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Core.UseProjectTeam(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWriting; return "", nil })
	m, _ := a.mediumFor(ctx, p, task.Playbook)
	if err := a.write(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0]); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	if len(task.Pending(1)) != 0 || task.Unreachable[0].Source != "review" {
		t.Fatal(task)
	}
	if err := a.escalate(ctx, p, task, nil, changes); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	d := openDecision(t, a, task)
	if d.OwnerStep != nil || !slices.Contains(d.Choices, choiceAcceptFollowUp) || len(task.Design) != 0 {
		t.Fatal(task, d)
	}
}

func TestAssetReportsRequireProductionBeforeSnapshot(t *testing.T) {
	t.Parallel()
	task := core.Task{Roles: []core.Role{{Name: "Dee", Kinds: []string{core.RoleDesigner}}}}
	for _, c := range []struct {
		name, block string
		want        bool
	}{
		{"frames", "[{\"requirement\":\"Generate every animation frame\",\"why\":\"sandbox\",\"asset_creation\":true}]", true},
		{"decoding", "[{\"requirement\":\"Decode images\",\"why\":\"sandbox\",\"asset_creation\":false}]", false},
		{"unknown", "[{\"requirement\":\"Frames\",\"why\":\"sandbox\"}]", true},
		{"malformed", "[", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := writerReply{unmet: c.block}
			if got := writerAssetError(task, in) != nil; got != c.want {
				t.Fatalf("error %v", got)
			}
			if writerAssetError(core.Task{}, in) != nil {
				t.Fatal("no designer changed")
			}
		})
	}
	in := parseWriterReply("```production\nframe: PNG, 32x32, reference attached\n```", true)
	if writerAssetError(task, in) != nil || len(in.assets) != 1 {
		t.Fatal(in)
	}
	for _, fresh := range []bool{true, false} {
		if !strings.Contains(writerPrompt(core.Project{}, task, "", fresh), "animation behaviour") {
			t.Fatal("missing rule")
		}
	}
	if assetRule(core.Task{}) != "" {
		t.Fatal("rule without designer")
	}
	task.Unreachable = []core.Unreachable{{Criterion: "Every frame", Why: "sandbox", Routed: "Writer: use designer"}}
	for _, fresh := range []bool{true, false} {
		prompt := writerPrompt(core.Project{}, task, "", fresh)
		if !strings.Contains(prompt, "Required before another draft: Every frame") || !strings.Contains(prompt, "provenance.json") {
			t.Fatal(prompt)
		}
	}
	p := core.Project{Playbook: &core.Playbook{Roles: task.Roles}}
	if !strings.Contains(pmPrompt(core.Snapshot{}, p), "animation behaviour") || !strings.Contains(pmTeamLine(p), "generation constraints") {
		t.Fatal("PM rule missing")
	}
	if strings.Contains(pmTeamLine(core.Project{}), "production hand-off") {
		t.Fatal("PM instruction without designer")
	}
}

func TestCodeOnlyReviewClassificationResumesRoundLimitDecision(t *testing.T) {
	t.Parallel()
	for _, pm := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent PM", true: "failed PM"}[pm], func(t *testing.T) {
			runner := &scriptedRunner{escalate: []string{"invalid", "invalid", "invalid", "invalid"}}
			a, p, task := draftTeam(t, runner, pm, "1")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Criteria = []string{"Animation timing remains accurate"}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Animation", Note: "Change animation timing"}}}}
			if err = a.escalate(ctx, p, task, nil, changes); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if task.Status != core.TaskWriting || len(task.Unreachable) != 1 || task.Unreachable[0].Source != "review" {
				t.Fatal(task)
			}
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: "```owner-step\n[{\"requirement\":\"Animation\",\"why\":\"animation timing is code\",\"asset_creation\":false}]\n```"}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			if len(task.Pending(1)) != 0 || task.Status != core.TaskDeciding || len(task.Revisions) != 1 {
				t.Fatal(task)
			}
			if err = a.escalate(ctx, p, task, nil, changes); err != nil {
				t.Fatal(err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			task, _ = snap.FindTask(task.ID)
			d := openDecision(t, a, task)
			if d.OwnerStep != nil || !strings.Contains(strings.Join(d.Choices, ","), choiceAcceptFollowUp) || len(task.Design) != 0 {
				t.Fatal(task, d)
			}
		})
	}
}

func TestBusyPMDoesNotEscalateAnUnclassifiedReport(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, task := draftTeam(t, runner, true, "2")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		t.Status = core.TaskDeciding
		t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "sandbox", Revision: 1}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.QueueTaskAs(ctx, p.ID, core.TaskInput{Objective: "Other work"}, core.LinkedByPM); err != nil {
		t.Fatal(err)
	}
	admit := func(core.Role) string { return "" }
	look, _, ok, err := a.Core.ClaimPM(ctx, p.ID, admit)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer a.Core.ReleaseProjectClaim(ctx, p.ID, look.Token)
	scheduled, err := a.Core.Schedule(ctx, admit)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for _, turn := range scheduled {
		if turn.Task.ID == task.ID {
			token = turn.Claim.Token
		}
	}
	if token == "" {
		t.Fatal("deciding was not claimed")
	}
	defer a.Core.ReleaseClaim(ctx, task.ID, token)
	if err = a.proposeOwnerStep(core.Fenced(ctx, task.ID, token), p, task, task.Unreachable[0]); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	got, _ := snap.FindTask(task.ID)
	if got.Status != core.TaskDeciding || got.DecisionID != "" || len(got.Unreachable) != 1 || !strings.Contains(got.Detail, "busy") {
		t.Fatal(got)
	}
}

func TestTeamChangeBeforeSpecReportsTheMissingDesigner(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{}
	a, p, task := draftTeam(t, runner, false, "2")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
	ctx := context.Background()
	task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
		yes := true
		t.Status = core.TaskDeciding
		t.Criteria = []string{"Frames"}
		t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "need generated assets", AssetCreation: &yes, Revision: 1}}
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err = a.Core.RouteAsset(ctx, task, task.Unreachable[0], "Writer")
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	p, _ = findProject(snap, p.ID)
	pb := *p.Playbook
	pb.Roles = slices.DeleteFunc(slices.Clone(pb.Roles), func(r core.Role) bool { return r.Holds(core.RoleDesigner) })
	if _, err = a.Core.SetPlaybook(ctx, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWaiting; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Core.UseProjectTeam(ctx, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err = a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskWriting; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	m, err := a.mediumFor(ctx, p, task.Playbook)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.write(ctx, p, task, m, task.RolesOf(core.RoleImplementer)[0]); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	if task.Status != core.TaskDeciding || len(task.Unreachable) != 1 || !strings.Contains(task.Unreachable[0].Why, "no designer") || len(writerTurns(runner)) != 1 {
		t.Fatal(task)
	}
	if err = a.proposeOwnerStep(ctx, p, task, task.Unreachable[0]); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	task, _ = snap.FindTask(task.ID)
	d := openDecision(t, a, task)
	if d.OwnerStep == nil || d.OwnerStep.Criterion != "Frames" || !strings.Contains(d.Context, "no designer") {
		t.Fatal(task, d)
	}
}

func TestRecordedAssetReportRoutesInsteadOfEscalating(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for _, c := range []struct {
		name           string
		designer, pm   bool
		classification *bool
		reply          string
		routed         bool
	}{
		{"designer", true, false, &yes, "", true},
		{"no designer", false, false, &yes, "", false},
		{"no designer with PM", false, true, &yes, `{"owner_step":true,"reason":"sandbox","step":"Generate frames"}`, false},
		{"contradictory PM", true, true, nil, `{"asset_creation":true,"owner_step":true,"reason":"generate assets","step":"Generate frames"}`, true},
		{"failed PM", true, true, nil, "invalid", true},
		{"failed correction preserves PM asset classification", true, true, &no, `{"asset_creation":true,"owner_step":true}`, true},
		{"no PM unclassified", true, false, nil, "", true},
		{"code only", true, true, &no, `{"asset_creation":false,"owner_step":true,"reason":"needs owner machine","step":"Check decoding"}`, false},
		{"recorded code classified by PM", true, true, nil, `{"asset_creation":false,"owner_step":true,"reason":"needs owner machine","step":"Check decoding"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			runner := &scriptedRunner{ownerStep: []string{c.reply, c.reply}}
			a, p, task := draftTeam(t, runner, c.pm, "2")
			if c.designer {
				seatDesigner(t, a, p.ID)
			}
			ctx := context.Background()
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			u := core.Unreachable{Criterion: "Generate every animation frame", Why: "image generation unavailable in implementer sandbox", Revision: 1, AssetCreation: c.classification}
			if c.classification == &no {
				u.Criterion = "Decode an image on the owner machine"
			}
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskDeciding
				t.Unreachable = []core.Unreachable{u}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = a.proposeOwnerStep(ctx, p, task, u); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if c.routed {
				if got.Status != core.TaskWriting || got.DecisionID != "" || got.Unreachable[0].Routed == "" || len(got.Revisions) != len(task.Revisions) {
					t.Fatal(got)
				}
			} else if got.Status != core.TaskWaiting || len(got.Design) != 0 {
				t.Fatal(got)
			}
		})
	}
}

func TestAssetReportCorrectionRequestsProductionWithoutADraft(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{writerReplies: []string{
		"```owner-step\n[{\"requirement\":\"Generate every animation frame\",\"why\":\"generation unavailable in my sandbox\",\"asset_creation\":true}]\n```",
		productionBlock(1),
	}}
	a, p, task := draftTeam(t, runner, false, "2")
	seatDesigner(t, a, p.ID)
	task = stepUntil(t, a, task.ID, withDesigner)
	if task.DecisionID != "" || len(task.Revisions) != 0 || task.OpenDesign().Production == nil || len(task.Unreachable) != 0 || len(task.OpenDesign().Requirements) != 1 {
		t.Fatal(task)
	}
	turns := writerTurns(runner)
	if len(turns) != 2 || !strings.Contains(turns[1].Prompt, "asset creation belongs to the designer") || len(turns[1].Resume) == 0 {
		t.Fatal(turns)
	}
}

func TestMixedUnclassifiedCorrectionPreservesReports(t *testing.T) {
	t.Parallel()
	for _, correction := range []string{productionBlock(1), productionBlock(1) + "\n```owner-step\n[{\"requirement\":\"Timing\",\"why\":\"live test\",\"asset_creation\":false}]\n```"} {
		runner := &scriptedRunner{writerReplies: []string{"```owner-step\n[{\"requirement\":\"Frames\",\"why\":\"sandbox generator unavailable\"},{\"requirement\":\"Timing\",\"why\":\"owner machine\",\"asset_creation\":false}]\n```", correction}}
		a, p, task := draftTeam(t, runner, false, "2")
		seatDesigner(t, a, p.ID)
		task = stepUntil(t, a, task.ID, withDesigner)
		if len(task.Unreachable) != 1 || task.Unreachable[0].Criterion != "Timing" || task.Unreachable[0].AssetCreation == nil || *task.Unreachable[0].AssetCreation || len(task.OpenDesign().Requirements) != 1 || task.OpenDesign().Requirements[0] != "Frames" {
			t.Fatal(task)
		}
		snap, _ := a.Core.Snapshot(context.Background())
		found := false
		for _, e := range snap.Activity {
			if strings.Contains(e.Summary, "Frames: asset creation belongs to the designer") {
				found = true
			}
		}
		if !found {
			t.Fatal("routing reason missing")
		}
	}
}

func TestProductionSpecLinksSelectedReportsAndPreservesCodeCorrections(t *testing.T) {
	t.Parallel()
	for _, named := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated artwork with code correction", true: "named routed requirement"}[named], func(t *testing.T) {
			runner := &scriptedRunner{}
			a, p, task := draftTeam(t, runner, false, "2")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) {
				t.Status = core.TaskWriting
				yes := true
				t.Unreachable = []core.Unreachable{{Criterion: "Frames", Why: "generator", AssetCreation: &yes, Routed: "Writer: production required", Revision: 1}, {Criterion: "Decode images", Why: "sandbox", Routed: "PM: classify", Revision: 1}}
				return "", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			block := productionBlock(1)
			if named {
				block = strings.Replace(block, "Match the existing art.", "Match the existing art.\nRequirement: Frames", 1)
			} else {
				block += "\n```owner-step\n[{\"requirement\":\"Decode images\",\"why\":\"live machine check\",\"asset_creation\":false}]\n```"
			}
			m, err := a.mediumFor(ctx, p, task.Playbook)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.recordDraft(ctx, p, task, m, "Writer", roles.Result{Text: block}, 0, nil); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if got.Status != core.TaskDesigning {
				t.Fatal(got)
			}
			if named {
				if len(got.Unreachable) != 1 || got.Unreachable[0].Criterion != "Decode images" || len(got.OpenDesign().Requirements) != 1 || got.OpenDesign().Requirements[0] != "Frames" {
					t.Fatal(got)
				}
			} else {
				if len(got.OpenDesign().Requirements) != 0 || len(got.Unreachable) != 2 || got.Unreachable[0].Routed == "" || got.Unreachable[1].AssetCreation == nil || *got.Unreachable[1].AssetCreation || got.Unreachable[1].Routed != "" {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestCombinedImplementerDesignerCanRequestProduction(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{writerReplies: []string{"```owner-step\n[{\"requirement\":\"Frames\",\"why\":\"need generated art\",\"asset_creation\":true}]\n```", productionBlock(1)}}
	a, p, task := draftTeam(t, runner, false, "2")
	pb := *p.Playbook
	pb.Roles[0].Kinds = append(pb.Roles[0].Kinds, core.RoleDesigner)
	if _, err := a.Core.SetPlaybook(context.Background(), p.ID, pb); err != nil {
		t.Fatal(err)
	}
	task = stepUntil(t, a, task.ID, withDesigner)
	if task.Checking != "Writer" || task.OpenDesign().Production == nil || task.DecisionID != "" || task.Failures != 0 {
		t.Fatal(task)
	}
}

func TestPMDecisionPromptsAndExamplesUsePinnedDesigner(t *testing.T) {
	t.Parallel()
	with := core.Task{Roles: []core.Role{{Name: "Dee", Kinds: []string{core.RoleDesigner}}}, Revisions: []core.Revision{{N: 1}}}
	p := core.Project{Playbook: &core.Playbook{Land: core.LandPolicy{}}}
	for _, task := range []core.Task{with, {Revisions: with.Revisions}} {
		for _, prompt := range []string{routePrompt(p, task, nil, []string{core.NextRevise, core.NextLand}), pmLandingPrompt(core.Snapshot{}, p, task, task.Revisions[0]), ownerStepPrompt(p, task, core.Unreachable{}), escalationPrompt(core.Snapshot{}, p, task, nil, nil, true, nil)} {
			if strings.Contains(prompt, "animation behaviour") != hasProductionDesigner(task) {
				t.Fatal(prompt)
			}
			if hasProductionDesigner(task) {
				for _, want := range []string{"generation constraints", "provenance.json", "as often as needed"} {
					if !strings.Contains(prompt, want) {
						t.Fatal(want, prompt)
					}
				}
			}
		}
		writer := writerPrompt(p, task, "", true)
		owner := ownerStepPrompt(p, task, core.Unreachable{})
		escalate := escalationPrompt(core.Snapshot{}, p, task, nil, nil, true, nil)
		if strings.Contains(writer, `"asset_creation": false`) != hasProductionDesigner(task) || strings.Contains(owner, `"asset_creation": true or false`) != hasProductionDesigner(task) || strings.Contains(escalate, `"asset_creation": false`) != hasProductionDesigner(task) {
			t.Fatal("inconsistent conditional JSON example")
		}
	}
}

func TestRoundLimitAssetFindingReturnsToWriting(t *testing.T) {
	t.Parallel()
	for _, asset := range []bool{true, false} {
		t.Run(map[bool]string{true: "asset", false: "code"}[asset], func(t *testing.T) {
			runner := &scriptedRunner{escalate: []string{strings.Replace(judgement(true, false, false, false, false), `"finding": 1`, map[bool]string{true: `"finding": 1, "asset_creation": true`, false: `"finding": 1, "asset_creation": false`}[asset], 1)}}
			a, p, task := draftTeam(t, runner, true, "1")
			seatDesigner(t, a, p.ID)
			task = stepUntil(t, a, task.ID, func(t core.Task) bool { return len(t.Revisions) > 0 })
			ctx := context.Background()
			task, err := a.Core.UpdateTask(ctx, task.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskDeciding; return "", nil })
			if err != nil {
				t.Fatal(err)
			}
			changes := []core.Verdict{{Role: "Reviewer", Findings: []core.Finding{{Criterion: "Animation", Note: "Generate frames"}}}}
			if !asset {
				changes[0].Findings[0].Note = "Change animation timing"
			}
			if err := a.escalate(ctx, p, task, nil, changes); err != nil {
				t.Fatal(err)
			}
			snap, _ := a.Core.Snapshot(ctx)
			got, _ := snap.FindTask(task.ID)
			if asset {
				if got.Status != core.TaskWriting || got.DecisionID != "" {
					t.Fatal(got)
				}
			} else if got.Status != core.TaskWaiting || len(got.Design) != 0 {
				t.Fatal(got)
			}
		})
	}
}

func TestProductionCoverageUsesCompleteReportIdentity(t *testing.T) {
	t.Parallel()
	yes := true
	for _, same := range []bool{false, true} {
		criteria := []string{"Blue frames", "Red frames"}
		if same {
			criteria = []string{"Animation", "Animation"}
		}
		task := core.Task{Roles: []core.Role{{Name: "D", Kinds: []string{core.RoleDesigner}}}, Unreachable: []core.Unreachable{
			{ID: "a", Criterion: criteria[0], Why: "blue", AssetCreation: &yes, Routed: "designer"},
			{ID: "b", Criterion: criteria[1], Why: "red", AssetCreation: &yes, Routed: "designer"},
		}}
		in := parseWriterReply("```production\nblue: PNG\n\nRequirement: Frames\n```", true)
		if !same {
			if _, err := mergeWriterReports(task, "", in); err == nil {
				t.Fatal("ambiguous coverage accepted")
			}
		}
		in.requirements = []string{"a", "b"}
		block, err := mergeWriterReports(task, "", in)
		if err != nil {
			t.Fatal(err)
		}
		reports, _ := parseOwnerSteps(block, 1, criteria, nil, nil)
		if links := productionLinks(in, reports); !slices.Equal(links, []string{"a", "b"}) {
			t.Fatal(links, block)
		}
		in.requirements = []string{"b"}
		in.unmet = fmt.Sprintf(`[{"report_id":"a","requirement":%q,"why":"code","asset_creation":false}]`, criteria[0])
		block, err = mergeWriterReports(task, "", in)
		if err != nil {
			t.Fatal(err)
		}
		reports, _ = parseOwnerSteps(block, 1, criteria, nil, nil)
		if links := productionLinks(in, reports); !slices.Equal(links, []string{"b"}) {
			t.Fatal(links, block)
		}
	}
}

func TestAmbiguousProductionCorrectionPreservesInitialReports(t *testing.T) {
	t.Parallel()
	task := core.Task{Roles: []core.Role{{Name: "D", Kinds: []string{core.RoleDesigner}}}}
	in := parseWriterReply("```production\nblue: PNG\n\nRequirement: Frames\n```", true)
	in.unmet = `[{"requirement":"Blue frames","why":"sandbox","asset_creation":true},{"requirement":"Red frames","why":"sandbox","asset_creation":true}]`
	block, err := mergeWriterReports(task, "", in)
	if err == nil {
		t.Fatal("ambiguous coverage accepted")
	}
	original, _ := ownerStepEntries(block)
	if len(original) != 2 {
		t.Fatal("correction lost original reports", block)
	}
	in = parseWriterReply("```production\nblue: PNG\n\nRequirement: Blue frames\n```", true)
	in.unmet, err = mergeWriterReports(task, block, in)
	if err != nil {
		t.Fatal(err)
	}
	reports, _ := parseOwnerSteps(in.unmet, 1, []string{"Blue frames", "Red frames"}, nil, nil)
	if len(reports) != 2 || !slices.Equal(productionLinks(in, reports), []string{"Blue frames"}) {
		t.Fatal(reports)
	}
}
