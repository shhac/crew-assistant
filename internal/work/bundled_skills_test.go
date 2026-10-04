package work

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/bundledskills"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
)

func assertBundledSkill(t *testing.T, spec roles.Spec, kind string) {
	t.Helper()
	entries, digest := bundledskills.Selection(kind, nil)
	if len(spec.Skills.Provided) != len(entries) {
		t.Fatalf("%s skills: %+v", kind, spec.Skills)
	}
	if len(entries) == 0 {
		return
	}
	if spec.Skills.Provided[0].Name != entries[0].Name || spec.Skills.Provided[0].Scripts || spec.Skills.Delivery != harness.SkillDeliveryComposed || !strings.Contains(spec.Instructions, bundledskills.Fingerprint(digest)) {
		t.Fatal(spec.Skills, spec.Instructions)
	}
	if _, err := os.ReadFile(spec.Skills.Provided[0].Dir + "/SKILL.md"); err != nil {
		t.Fatal(err)
	}
}

func TestRetiredSpriteTurnSurvivesDeletionAndRestart(t *testing.T) {
	var reopen func(*Loop) *Loop
	lp, _, task := loopApp(t, &scriptedRunner{}, "", &reopen)
	ctx := context.Background()
	in := core.MemberInput{Name: "Ash", Kinds: []string{core.RoleDesigner}, Engine: "codex", Instructions: core.LegacySpriteInstructions}
	m, err := lp.Core.SaveMember(ctx, "", in)
	if err != nil {
		t.Fatal(err)
	}
	seat := core.Role{Name: m.Name, Member: m.ID, Kinds: m.Kinds, Engine: m.Engine, Instructions: "Keep this\n\n" + m.Instructions}
	expected := in.Instructions
	// The live member loses the role; pinned designer seats still need retirement.
	in.Kinds = []string{core.RoleImplementer}
	if _, err := lp.Core.SaveMember(ctx, m.ID, in); err != nil {
		t.Fatal(err)
	}
	in.Instructions, in.ExpectedInstructions = "", &expected
	if _, err := lp.Core.SaveMember(ctx, m.ID, in); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	prepare := func() roles.Spec {
		t.Helper()
		spec, cleanup, err := lp.roleSpec(task, seat, work, false, docsMedium{}, "review", nil, core.RoleDesigner)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if strings.Contains(spec.Instructions, core.LegacySpriteInstructions) || !strings.Contains(spec.Instructions, "Keep this") {
			t.Fatal(spec.Instructions)
		}
		assertBundledSkill(t, spec, core.RoleDesigner)
		return spec
	}
	before := prepare()
	lp = reopen(lp)
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := snap.Member(m.ID)
	if !reflect.DeepEqual(current.Kinds, []string{core.RoleImplementer}) || current.Instructions != "" {
		t.Fatal("cleanup or restart restored member fields", current)
	}
	if after := prepare(); before.Instructions != after.Instructions {
		t.Fatal("restart restored pinned lessons")
	}
	if err := lp.Core.DeleteMember(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	lp = reopen(lp)
	task.Round++
	after := prepare()
	if before.Instructions != after.Instructions || !reflect.DeepEqual(before.Skills, after.Skills) {
		t.Fatal("deletion or restart changed resume inputs")
	}
	if seat.Instructions != "Keep this\n\n"+core.LegacySpriteInstructions {
		t.Fatal("historical seat changed")
	}
}

func TestRetirementLookupFailureStartsNoRole(t *testing.T) {
	runner := &scriptedRunner{}
	lp, _, task := loopApp(t, runner, "")
	// A closed store cannot read state. All baseSpec callers must refuse
	// to launch, including paths that do not use roleSpec (PM and release QA).
	st, err := core.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	lp.Core = core.NewService(st, lp.Config())
	seat := core.Role{Member: "synthetic", Engine: "codex", Instructions: core.LegacySpriteInstructions}
	spec := lp.baseSpec(seat, t.TempDir(), "go")
	if spec.PreparationError == nil {
		t.Fatal("lookup failure ignored")
	}
	if _, err := lp.runRole(context.Background(), spec); err == nil {
		t.Fatal("launched despite lookup failure")
	}
	if _, _, err := lp.roleSpec(task, seat, t.TempDir(), false, docsMedium{}, "go", nil); err == nil {
		t.Fatal("roleSpec ignored lookup failure")
	}
	if len(runner.seen) != 0 {
		t.Fatal("runner invoked")
	}
}

func TestPerformedRoleBundledDeliveryPinnedDisableAndStableRounds(t *testing.T) {
	lp, p, task := loopApp(t, &scriptedRunner{}, "")
	work := t.TempDir()
	r := core.Role{Name: "Many roles", Kinds: []string{core.RoleResearcher, core.RoleDesigner, core.RoleImplementer}, Engine: "codex"}
	task.Playbook = p.Playbook
	for _, kind := range []string{core.RoleDesigner, core.RoleImplementer, core.RoleResearcher, core.RoleReviewer, core.RoleQA, core.RolePM} {
		task.Status = core.TaskWriting // Explicit performed role must take precedence.
		spec, cleanup, err := lp.roleSpec(task, r, work, false, docsMedium{}, "prompt", nil, kind)
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		assertBundledSkill(t, spec, kind)
		task.Round++
		restart := New(lp.Core, lp.Config, false)
		next, clean, err := restart.roleSpec(task, r, work, false, docsMedium{}, "different prompt", nil, kind)
		if err != nil {
			t.Fatal(err)
		}
		clean()
		if next.Instructions != spec.Instructions || !reflect.DeepEqual(next.Skills, spec.Skills) {
			t.Fatal("round or restart changed fingerprint")
		}
	}
	// Change project settings after pinning; this turn keeps the old selection.
	if _, err := lp.Core.SetBundledSkill(context.Background(), p.ID, "sprite-atlas", false); err != nil {
		t.Fatal(err)
	}
	spec, clean, err := lp.roleSpec(task, r, work, false, docsMedium{}, "prompt", nil, core.RoleDesigner)
	if err != nil {
		t.Fatal(err)
	}
	clean()
	assertBundledSkill(t, spec, core.RoleDesigner)
	pb := *task.Playbook
	pb.DisabledBundledSkills = []string{"sprite-atlas"}
	task.Playbook = &pb
	spec, clean, err = lp.roleSpec(task, r, work, false, docsMedium{}, "prompt", nil, core.RoleDesigner)
	if err != nil {
		t.Fatal(err)
	}
	clean()
	if len(spec.Skills.Provided) != 0 || strings.Contains(spec.Instructions, "Bundled skill contents:") {
		t.Fatal("disabled skill delivered")
	}
	pb.DisabledBundledSkills = []string{"sprite-atlas-pipeline"}
	spec, clean, err = lp.roleSpec(task, r, work, false, docsMedium{}, "prompt", nil, core.RoleDesigner)
	if err != nil {
		t.Fatal(err)
	}
	clean()
	assertBundledSkill(t, spec, core.RoleDesigner)
}

func TestSkillPublicationFailureStartsNoRole(t *testing.T) {
	runner := &scriptedRunner{}
	lp, _, task := loopApp(t, runner, "")
	if err := os.WriteFile(lp.Core.StateDirectory()+"/bundled-skills", []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := lp.roleSpec(task, core.Role{Name: "Writer", Engine: "claude"}, t.TempDir(), true, docsMedium{}, "prompt", nil, core.RoleImplementer)
	if err == nil || len(runner.seen) != 0 {
		t.Fatal("failed skill admission started work", err)
	}
}

func TestTeamChoiceRetainsIndependentSkillSettings(t *testing.T) {
	lp, p, _ := loopApp(t, &scriptedRunner{}, "")
	var err error
	p, err = lp.Core.SetBundledSkill(context.Background(), p.ID, "sprite-atlas", false)
	if err != nil {
		t.Fatal(err)
	}
	p, err = lp.SetTeam(context.Background(), p.ID, TeamChoice{Template: "draft", WriterEngine: "claude", ReviewerEngine: "codex", MaxRounds: "3"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Playbook.DisabledBundledSkills, []string{"sprite-atlas"}) {
		t.Fatal(p.Playbook)
	}
}
