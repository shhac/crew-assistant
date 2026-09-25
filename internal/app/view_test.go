package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestEveryAssistantToolHasALabelTheOwnerCanRead(t *testing.T) {
	for _, tool := range engine.Tools() {
		if label, ok := engine.ToolLabel(tool.Function.Name); !ok || label == "" {
			t.Errorf("%s has no label, so any turn that uses it fails", tool.Function.Name)
		}
	}
}

func TestTheAssistantSeesAnOverviewAndReadsDetailOnlyWhenItAsks(t *testing.T) {
	long := strings.Repeat("x", 5000)
	var s core.Snapshot
	for i := 0; i < 40; i++ {
		task := core.Task{ID: fmt.Sprint("done", i), Status: core.TaskLanded, Objective: "done", UpdatedAt: time.Unix(int64(i), 0)}
		for n := 1; n <= 5; n++ {
			task.Revisions = append(task.Revisions, core.Revision{N: n, Summary: long})
			task.Verdicts = append(task.Verdicts, core.Verdict{Revision: n, Summary: long, Findings: []core.Finding{{Note: long}}})
		}
		s.Tasks = append(s.Tasks, task)
		s.Decisions = append(s.Decisions, core.Decision{ID: fmt.Sprint("old", i), Status: "resolved", Context: long})
	}
	s.Tasks = append(s.Tasks, core.Task{ID: "live", Status: core.TaskReviewing, Stage: core.StageReviewing, Round: 3, Checking: "Rune", Criteria: []string{long}, Plan: &core.Plan{Summary: long}, Revisions: []core.Revision{{N: 1, Summary: "first"}, {N: 2, Summary: "second"}, {N: 3, Summary: "third"}}, Verdicts: []core.Verdict{{Revision: 2, Summary: "old"}, {Revision: 3, Summary: "current"}}, Messages: []core.TeamMessage{{Text: long}}})
	s.Projects = append(s.Projects, core.Project{ID: "p", Playbook: &core.Playbook{Roles: []core.Role{{Name: "Ada", Kinds: []string{core.RoleImplementer}, Instructions: long}}}})
	s.Decisions = append(s.Decisions, core.Decision{ID: "now", Status: "open", Context: long})
	for i := 0; i < 40; i++ {
		s.Activity = append(s.Activity, core.Activity{ID: fmt.Sprint("a", i), Summary: "newest first"})
	}
	view := assistantView(s)
	raw, _ := json.Marshal(view)
	if len(raw) > 16<<10 {
		t.Fatalf("the assistant's view is %d bytes", len(raw))
	}
	live := view.Tasks[len(view.Tasks)-1]
	if live.Stage != core.StageReviewing || live.Round != 3 || live.Checking != "Rune" {
		t.Fatalf("the live task lost where it stands: %+v", live)
	}
	if len(live.Revisions)+len(live.Verdicts)+len(live.Messages)+len(live.Criteria) != 0 || live.Plan != nil {
		t.Fatalf("how the live task is being built reached the overview: %+v", live)
	}
	if view.Projects[0].Playbook.Roles[0].Instructions != "" || s.Projects[0].Playbook.Roles[0].Instructions != long {
		t.Fatal("the team's instructions reached the overview, or the state was changed")
	}
	last := view.Decisions[len(view.Decisions)-1]
	if last.ID != "now" || last.Context != long {
		t.Fatal("an open decision was cut")
	}
	if len(view.Activity) != 30 || view.Activity[0].ID != "a0" {
		t.Fatalf("the assistant should see the newest activity: %d from %s", len(view.Activity), view.Activity[0].ID)
	}
	if len(s.Tasks[0].Revisions) != 5 {
		t.Fatal("the view changed the state it was made from")
	}
	var finished []core.Task
	for _, task := range view.Tasks {
		if task.Finished() {
			finished = append(finished, task)
		}
	}
	if len(finished) != shownFinished || len(finished[0].Revisions) != 0 || finished[0].Objective != "done" {
		t.Fatalf("finished tasks should be a few, by outcome only: %d, %+v", len(finished), finished[0])
	}
	detail := taskDetail(s.Tasks[0])
	if len(detail.Revisions) != 3 || len(detail.Verdicts) != 5 || detail.Revisions[2].N != 5 {
		t.Fatalf("read_task should give the latest drafts and every review: %d drafts, %d reviews", len(detail.Revisions), len(detail.Verdicts))
	}
}

func TestTheAssistantDoesNotCarryItsOwnPictureIntoEveryTurn(t *testing.T) {
	drawn := config.Avatar{Background: "#101820", Accent: "#ffffff", Marks: []config.Mark{{D: "M10 10L118 118", Color: "#ffffff", StrokeWidth: 8}}}
	svg, _ := drawn.SVG()
	learnings := make([]core.Learning, 12)
	for i := range learnings {
		learnings[i] = core.Learning{ID: fmt.Sprint(i), Text: fmt.Sprint("learning ", i)}
	}
	view := assistantView(core.Snapshot{Assistant: core.Assistant{Name: "Iris", Avatar: drawn, AvatarSVG: svg}, Members: []core.Member{{ID: "m", Name: "Ada", Avatar: drawn, AvatarSVG: svg, Learnings: learnings}}})
	if m := view.Members[0]; m.Name != "Ada" || m.AvatarSVG != "" || len(m.Learnings) != 5 || m.Learnings[4].Text != "learning 11" {
		t.Fatalf("member view %+v", m)
	}
	raw, _ := json.Marshal(view)
	if view.Assistant.Name != "Iris" || strings.Contains(string(raw), "M10 10") || strings.Contains(string(raw), "<svg") {
		t.Fatalf("assistant view: %s", raw)
	}
}
