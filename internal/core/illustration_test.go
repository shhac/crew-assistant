package core

import "testing"

func TestNamedDesignerWaitsAndRemovedSeatFallsBack(t *testing.T) {
	for _, mode := range []string{"free", "busy", "usage", "removed", "changed kind"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t)
			p := plannedProject(t, s)
			pb := *p.Playbook
			pb.MaxActive = 0
			vector := Role{Name: "Vector", Engine: "claude", Kinds: []string{RoleDesigner}}
			raster := Role{Name: "Raster", Engine: "codex", Kinds: []string{RoleDesigner}}
			pb.Roles = append(pb.Roles, vector, raster)
			if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
				t.Fatal(err)
			}
			tasks := queueAll(t, s, p, "A", "B")
			for _, task := range tasks {
				if _, err := s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) {
					t.Status = TaskResearching
					t.Roles = pb.Roles
					return "", nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			ask := DesignAsk{From: "Researcher", For: "Raster", Question: "Make a robin", Owner: DecisionInput{Title: "Art?", Context: "Robin", Recommendation: "Draw it", Choices: []string{"Go", "Stop"}}}
			held, err := s.AskDesign(testContext, tasks[0].ID, ask)
			if err != nil {
				t.Fatal(err)
			}
			if held.Design[0].For != "Raster" || held.Checking != "Raster" {
				t.Fatalf("%+v", held)
			}
			if mode == "busy" {
				if _, err := s.AskDesign(testContext, tasks[1].ID, ask); err != nil {
					t.Fatal(err)
				}
			} else {
				finish(t, s, tasks[1].ID, TaskStopped)
			}
			if mode == "removed" || mode == "changed kind" {
				_, err := s.UpdateTask(testContext, tasks[0].ID, func(task *Task, _ *Project) (string, error) {
					task.Roles = []Role{vector}
					if mode == "changed kind" {
						raster.Kinds = []string{RoleImplementer}
						task.Roles = append(task.Roles, raster)
					}
					return "", nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			admit := anyone
			if mode == "usage" {
				admit = func(r Role) string {
					if r.Name == "Raster" {
						return "usage limit"
					}
					return ""
				}
			}
			out, err := s.Schedule(testContext, admit)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "usage" {
				if len(out) != 0 {
					t.Fatalf("claimed while waiting: %+v", out)
				}
				return
			}
			if len(out) != 1 {
				t.Fatalf("claims: %+v", out)
			}
			want := "Raster"
			if mode == "removed" || mode == "changed kind" {
				want = "Vector"
			}
			if out[0].Seat.Name != want {
				t.Fatalf("claimed %s, want %s", out[0].Seat.Name, want)
			}
			if out[0].Task.Detail != "With "+want+" for design input" || out[0].Task.Checking != want {
				t.Fatalf("design claim still shows the previous seat: %+v", out[0].Task)
			}
			if mode == "busy" {
				again, err := s.Schedule(testContext, anyone)
				if err != nil || len(again) != 0 {
					t.Fatalf("free Vector claimed: %+v %v", again, err)
				}
			}
		})
	}
}
