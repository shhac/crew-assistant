package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

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

func TestTheAssistantSeesWhatItCanActOnAndOnlyTheOutcomeOfWhatIsDone(t *testing.T) {
	long := strings.Repeat("x", 5000)
	var s core.Snapshot
	for i := 0; i < 40; i++ {
		task := core.Task{ID: fmt.Sprint("done", i), Status: core.TaskLanded, Objective: "done"}
		for n := 1; n <= 5; n++ {
			task.Revisions = append(task.Revisions, core.Revision{N: n, Summary: long})
			task.Verdicts = append(task.Verdicts, core.Verdict{Revision: n, Summary: long, Findings: []core.Finding{{Note: long}}})
		}
		s.Tasks = append(s.Tasks, task)
		s.Decisions = append(s.Decisions, core.Decision{ID: fmt.Sprint("old", i), Status: "resolved", Context: long})
	}
	s.Tasks = append(s.Tasks, core.Task{ID: "live", Status: core.TaskReviewing, Revisions: []core.Revision{{N: 1, Summary: "first"}, {N: 2, Summary: "second"}, {N: 3, Summary: "third"}}, Verdicts: []core.Verdict{{Revision: 2, Summary: "old"}, {Revision: 3, Summary: "current"}}})
	s.Decisions = append(s.Decisions, core.Decision{ID: "now", Status: "open", Context: long})
	view := assistantView(s)
	raw, _ := json.Marshal(view)
	if len(raw) > 64<<10 {
		t.Fatalf("the assistant's view is %d bytes", len(raw))
	}
	live := view.Tasks[len(view.Tasks)-1]
	if len(live.Revisions) != 2 || len(live.Verdicts) != 1 || live.Verdicts[0].Summary != "current" {
		t.Fatalf("the live task lost what the assistant acts on: %+v", live)
	}
	last := view.Decisions[len(view.Decisions)-1]
	if last.ID != "now" || last.Context != long {
		t.Fatal("an open decision was cut")
	}
	if len(s.Tasks[0].Revisions) != 5 {
		t.Fatal("the view changed the state it was made from")
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
