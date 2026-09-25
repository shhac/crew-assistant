package sample

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/avatars"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

func TestTheSampleShowsWorkAtEveryStageAndNeverTouchesRealState(t *testing.T) {
	dir := t.TempDir()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := core.NewService(store, config.Default())
	ctx := context.Background()
	if err = Seed(ctx, s, filepath.Join(dir, "sample")); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(ctx)
	stages := map[string]bool{}
	for _, task := range snap.Tasks {
		stages[task.Stage] = true
	}
	for _, want := range []string{core.StageTodo, core.StagePlanning, core.StageImplementing, core.StageReviewing, core.StageQA, core.StageReady, core.StageDone} {
		if !stages[want] {
			t.Errorf("no sample task is at %s", want)
		}
	}
	if len(snap.Members) == 0 {
		t.Error("the sample has no team members")
	}
	if !slices.ContainsFunc(snap.Projects, func(p core.Project) bool {
		_, ok := p.PMSeat()
		return ok && p.OrderedBy == core.OrderedByPM
	}) {
		t.Error("no sample project has a PM keeping its list")
	}
	for _, m := range snap.Members {
		if !strings.HasPrefix(m.AvatarSVG, "<svg") {
			t.Errorf("member %s cannot be drawn: %+v", m.Name, m.Avatar)
		}
		if _, ok := avatars.NewStore(s.StateDirectory()).Path(m.Avatar.Image, "small"); !ok || m.Avatar.Look == "" {
			t.Errorf("member %s has no drawn face: %+v", m.Name, m.Avatar)
		}
	}
	for _, task := range snap.Tasks {
		for _, v := range task.Verdicts {
			if _, ok := task.Role(v.Role); !ok {
				t.Errorf("%q has a verdict from %s, who is not on its team", task.Objective, v.Role)
			}
		}
	}
	for _, d := range snap.Decisions {
		if d.Status == "open" {
			if task := findTask(snap, d.TaskID); task.DecisionID != d.ID || task.Status != core.TaskWaiting {
				t.Errorf("decision %q holds no waiting task", d.Title)
			}
		}
	}
	for _, p := range snap.Projects {
		if p.ScratchDirectory == "" {
			t.Fatalf("project %q has no folder of its own", p.Title)
		}
		if p.Title != "Q4 planning memo" {
			continue
		}
		docs, _ := localdocs.Open(p.ScratchDirectory)
		files, err := docs.Preview("demo-plan", 2, 1<<16)
		if err != nil || len(files) != 1 || !strings.Contains(files[0].Content, "Three priorities") {
			t.Fatalf("the plan's draft can't be read: %+v %v", files, err)
		}
	}
	if err = Seed(ctx, s, filepath.Join(dir, "again")); err == nil {
		t.Fatal("the sample was written over a state that already had something in it")
	}
}

func findTask(s core.Snapshot, id string) core.Task {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t
		}
	}
	return core.Task{}
}
