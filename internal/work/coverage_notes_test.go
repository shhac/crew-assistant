package work

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestCoverageNotesSurviveRealStoreLimits(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			ctx := context.Background()
			lp := testLoop(t)
			p, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Coverage", Template: "draft", Brief: core.BriefInput{Goal: "Coverage"}})
			if err != nil {
				t.Fatal(err)
			}
			task, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Check"})
			if err != nil {
				t.Fatal(err)
			}
			// Existing steps already fill the task's retention budget.
			for i := range 150 {
				if err := lp.Core.RecordTurnStep(ctx, core.TurnStep{TaskID: task.ID, Seat: "QA", Turn: "old", Item: fmt.Sprint(i), Kind: core.StepNote, Text: strings.Repeat("x", 16<<10)}); err != nil {
					t.Fatal(err)
				}
			}
			var input bytes.Buffer
			enc := json.NewEncoder(&input)
			for _, e := range []checktest.Event{{Action: "output", Package: "github.com/shhac/crew-assistant/internal/cli", Test: "TestGeneratedShellCompletionSyntax/fish", Output: "script generated; fish unavailable for syntax check"}, {Action: "skip", Package: "github.com/shhac/crew-assistant/internal/cli", Test: "TestGeneratedShellCompletionSyntax/fish"}} {
				if err := enc.Encode(e); err != nil {
					t.Fatal(err)
				}
			}
			count := 8
			if large {
				count = 900
			}
			for i := range count {
				name := fmt.Sprintf("TestRequired/%04d/", i) + strings.Repeat("i", 2029)
				for _, e := range []checktest.Event{{Action: "output", Package: "p", Test: name, Output: "required capability denied: " + strings.Repeat("r", 997)}, {Action: "skip", Package: "p", Test: name}} {
					if err := enc.Encode(e); err != nil {
						t.Fatal(err)
					}
				}
			}
			_ = enc.Encode(checktest.Event{Action: "pass", Package: "p"})
			report, readErr := checktest.Read(&input, "darwin")
			if large {
				if readErr == nil || !strings.Contains(readErr.Error(), "retention budget") || report.Complete || !report.Failed || len(report.Skips) >= count {
					t.Fatalf("budget not enforced: %d %v", len(report.Skips), readErr)
				}
			} else if readErr != nil {
				t.Fatal(readErr)
			}
			watch := lp.watchTurn(task, core.RoleQA, core.Role{Name: "QA"}, "", false)
			watch.Started()
			runs := &checkRuns{coverageNote: func(s string) { lp.commandNoteFor(watch, s) }}
			runs.noteCoverage(&checktest.Progress{Stage: "Go tests", Report: report}, 0, "Hosted check interrupted; coverage incomplete.")
			watch.Ended()
			steps, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, "QA")
			if err != nil {
				t.Fatal(err)
			}
			var kept strings.Builder
			for _, step := range steps {
				if step.Turn == "old" {
					continue
				}
				if step.Clipped {
					t.Fatalf("coverage note clipped: %s", step.Item)
				}
				kept.WriteString(step.Text)
			}
			for _, skip := range report.Skips {
				if !strings.Contains(kept.String(), skip.Test) || !strings.Contains(kept.String(), skip.Reason) {
					t.Fatalf("lost identifying evidence: %s", skip.Test)
				}
			}
			if !strings.Contains(kept.String(), fmt.Sprintf("observed skip count: %d", len(report.Skips))) || !strings.Contains(kept.String(), "complete: false") {
				t.Fatal("lost incomplete coverage count")
			}
			if !strings.Contains(kept.String(), "known skip: optional shell: fish") || (large && !strings.Contains(kept.String(), "retention budget reached")) {
				t.Fatal("lost known skip or capacity diagnostic")
			}
		})
	}
}
