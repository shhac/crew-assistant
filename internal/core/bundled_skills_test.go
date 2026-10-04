package core

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func TestBundledSkillSettingsIsolationPinningAndAdoption(t *testing.T) {
	s, c := fixture(t)
	p := newProject(t, s)
	other := newProject(t, s)
	if len(p.Playbook.DisabledBundledSkills) != 0 {
		t.Fatal("skills not enabled by default")
	}
	task := Task{ProjectID: p.ID}
	snap, _ := s.Snapshot(testContext)
	pinTeam(&snap, &p, &task)
	got, err := s.SetBundledSkill(testContext, p.ID, "sprite-atlas", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Playbook.DisabledBundledSkills) != 0 || !slices.Equal(got.Playbook.DisabledBundledSkills, []string{"sprite-atlas"}) {
		t.Fatal(task, got)
	}
	snap, _ = s.Snapshot(testContext)
	for _, project := range snap.Projects {
		if project.ID == other.ID && len(project.Playbook.DisabledBundledSkills) != 0 {
			t.Fatal("cross-project setting")
		}
	}
	if _, err := s.SetBundledSkill(testContext, p.ID, "unknown", false); err == nil {
		t.Fatal("unknown skill accepted")
	}
	if err := s.store.update(testContext, func(v *Snapshot) error {
		task.ID = "synthetic-task"
		task.Status = TaskWaiting
		v.Tasks = append(v.Tasks, task)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	adopted, err := s.UseProjectTeam(testContext, p.ID, task.ID)
	if err != nil || !slices.Equal(adopted.Playbook.DisabledBundledSkills, got.Playbook.DisabledBundledSkills) {
		t.Fatal(adopted, err)
	}
	// A new Service reads the persisted selection, not an in-memory catalog.
	st, err := Open(filepath.Join(s.StateDirectory(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	restarted := NewService(st, c)
	snap, err = restarted.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range snap.Projects {
		if project.ID == p.ID && !slices.Equal(project.Playbook.DisabledBundledSkills, []string{"sprite-atlas"}) {
			t.Fatal(project)
		}
	}
	got, err = s.SetBundledSkill(testContext, p.ID, "sprite-atlas", true)
	if err != nil || len(got.Playbook.DisabledBundledSkills) != 0 {
		t.Fatal(got, err)
	}
}

func TestBundledSkillConcurrentEditsAndIdempotentActivity(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	var wg sync.WaitGroup
	for _, name := range []string{"sprite-atlas", "sprite-atlas-pipeline"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.SetBundledSkill(testContext, p.ID, name, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := s.EditPlaybook(testContext, p.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error { pb.MaxActive = 2; return nil }); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	snap, _ := s.Snapshot(testContext)
	var found Project
	for _, v := range snap.Projects {
		if v.ID == p.ID {
			found = v
		}
	}
	if found.Playbook.MaxActive != 2 || len(found.Playbook.DisabledBundledSkills) != 2 {
		t.Fatal(found)
	}
	before := snap.Activity
	if _, err := s.SetBundledSkill(testContext, p.ID, "sprite-atlas", false); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if !reflect.DeepEqual(before, snap.Activity) {
		t.Fatal("replayed toggle added activity")
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := s.SetBundledSkill(ctx, p.ID, "sprite-atlas", true); err == nil {
		t.Fatal("cancelled transaction succeeded")
	}
	snap, _ = s.Snapshot(testContext)
	if !reflect.DeepEqual(before, snap.Activity) {
		t.Fatal("failed transaction added activity")
	}
}

func TestDisabledBundledSkillValidationAndEditCloning(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	for _, names := range [][]string{{"unknown"}, {"sprite-atlas", "sprite-atlas"}} {
		pb := *p.Playbook
		pb.DisabledBundledSkills = names
		if pb.Validate() == nil {
			t.Fatal(names)
		}
	}
	p, err := s.SetBundledSkill(testContext, p.ID, "sprite-atlas", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.EditPlaybook(testContext, p.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.DisabledBundledSkills[0] = "sprite-atlas-pipeline"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Playbook.DisabledBundledSkills[0] != "sprite-atlas" {
		t.Fatal("edit changed previous snapshot")
	}
}
