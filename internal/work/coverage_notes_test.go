//go:build !windows

package work

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/checktest"
	"github.com/shhac/crew-assistant/internal/core"
)

func completedCoverageRun(t *testing.T, lp *Loop, medium gitMedium, source string, report checktest.Report) *checkRuns {
	t.Helper()
	lp.commands = func(_ context.Context, opts sandbox.Options) (commandSandbox, error) {
		var path, invocation string
		for _, entry := range opts.Env {
			if value, ok := strings.CutPrefix(entry, checktest.ProgressEnv+"="); ok {
				path = value
			}
			if value, ok := strings.CutPrefix(entry, checktest.InvocationEnv+"="); ok {
				invocation = value
			}
		}
		return &fakeCommands{run: func(context.Context, sandbox.CommandRequest) (sandbox.CommandResult, error) {
			if err := checktest.SaveProgress(path, checktest.Progress{Invocation: invocation, Stage: "complete", Done: true, Report: report}); err != nil {
				return sandbox.CommandResult{}, err
			}
			return sandbox.CommandResult{Stdout: strings.Repeat("preceding stdout\n", 1400)}, nil
		}}, nil
	}
	runs := newCheckRuns(lp, medium, source, nil)
	t.Cleanup(runs.close)
	return runs
}

func TestCompletedCoverageSurvivesClippedToolPage(t *testing.T) {
	for _, count := range []int{0, 4, 150} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx := context.Background()
			lp, medium, source := checkFixture(t)
			p, err := lp.Core.CreateProject(ctx, core.ProjectInput{Title: "Coverage", Template: "draft", Brief: core.BriefInput{Goal: "Coverage"}})
			if err != nil {
				t.Fatal(err)
			}
			task, err := lp.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Check"})
			if err != nil {
				t.Fatal(err)
			}
			report := checktest.Report{Complete: true}
			for i := range count {
				report.Skips = append(report.Skips, checktest.Skip{Package: "fixture", Test: fmt.Sprintf("TestRequired/%03d", i), Reason: fmt.Sprintf("required capability denied %03d", i)})
			}
			if count > 0 {
				report.Failed = true
				report.Skips = append(report.Skips, checktest.Skip{Package: "github.com/shhac/crew-assistant/internal/cli", Test: "TestGeneratedShellCompletionSyntax/fish", Reason: "script generated; fish unavailable for syntax check", Exception: "optional shell: fish"})
			}
			watch := lp.watchTurn(task, core.RoleQA, core.Role{Name: "QA"}, "", false)
			watch.Started()
			runs := completedCoverageRun(t, lp, medium, source, report)
			runs.coverageNote = func(s string) { lp.commandNoteFor(watch, s) }
			content, err := runs.call(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if runs.current.delivered != (count <= 32) || runs.current.skipOffset == 0 && count > 0 {
				t.Fatal("paging precondition not met")
			}
			watch.Saw(session.Event{Kind: "tool_completed", ItemID: "check", Tool: "run_check", Output: content})
			watch.Ended()
			runs.close()
			steps, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, "QA")
			if err != nil {
				t.Fatal(err)
			}
			var notes strings.Builder
			toolFound := false
			for _, step := range steps {
				if step.Kind == core.StepTool {
					toolFound = true
					if !step.Clipped || strings.Contains(step.Output, "coverage") || strings.Contains(step.Output, "TestRequired") {
						t.Fatal("tool output was not clipped before coverage")
					}
				} else if step.Kind == core.StepNote {
					if step.Clipped {
						t.Fatal("coverage note clipped")
					}
					notes.WriteString(step.Text)
				}
			}
			if !toolFound {
				t.Fatal("missing persisted tool step")
			}
			for _, skip := range report.Skips {
				var entry bytes.Buffer
				skip.Write(&entry)
				if !strings.Contains(notes.String(), entry.String()) {
					t.Fatalf("lost evidence: %s", skip.Test)
				}
			}
			for _, want := range []string{"Hosted check finished", fmt.Sprintf("observed skip count: %d", len(report.Skips)), fmt.Sprintf("required-skip failures: %d", count), "complete: true"} {
				if !strings.Contains(notes.String(), want) {
					t.Fatal("missing", want)
				}
			}
			if strings.Contains(notes.String(), "before all check evidence was read") != (count > 32) {
				t.Fatal("incorrect unread status")
			}
			runs.close()
			again, err := lp.Core.TurnSteps(ctx, p.ID, task.ID, "QA")
			if err != nil || len(again) != len(steps) {
				t.Fatal("repeated close added notes", err)
			}
		})
	}
}

func TestCoverageNotesSurviveRealStoreLimits(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			ctx := context.Background()
			lp, medium, source := checkFixture(t)
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
			runs := completedCoverageRun(t, lp, medium, source, report)
			runs.coverageNote = func(s string) { lp.commandNoteFor(watch, s) }
			if _, err := runs.call(ctx); err != nil {
				t.Fatal(err)
			}
			watch.Ended()
			runs.close()
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
			if !strings.Contains(kept.String(), fmt.Sprintf("observed skip count: %d", len(report.Skips))) || !strings.Contains(kept.String(), fmt.Sprintf("complete: %t", report.Complete)) {
				t.Fatal("lost incomplete coverage count")
			}
			if !strings.Contains(kept.String(), "known skip: optional shell: fish") || (large && !strings.Contains(kept.String(), "retention budget reached")) {
				t.Fatal("lost known skip or capacity diagnostic")
			}
		})
	}
}
