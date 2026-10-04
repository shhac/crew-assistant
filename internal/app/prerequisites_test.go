package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestPrerequisiteComparisonUsesOnlyTaskRecordsAndNoTools(t *testing.T) {
	a := testApp(t)
	now := time.Now()
	settled := []core.Prerequisite{{What: "Someone can push a diagnostics PR to example/repo", Blocker: "settled", Outcome: "confirmed", Settlements: []core.PrerequisiteSettlement{{Outcome: "confirmed", Answer: "It's ready", By: core.FromOwner, At: now, Source: "decision"}}}}
	for _, result := range []string{"equivalent", "different", "uncertain"} {
		t.Run(result, func(t *testing.T) {
			calls := 0
			a.prerequisiteComplete = func(_ context.Context, cfg engine.Config, m []engine.Message, tools []engine.Tool) (engine.Message, engine.Usage, error) {
				calls++
				if len(tools) != 0 || len(m) != 2 || cfg.BeforeRequest == nil {
					t.Fatal("unbounded or tool-enabled comparison")
				}
				for _, want := range []string{"push is not merge", "required version", "repository", "scope", "timing", "WHOLE question", "owner authority"} {
					if !strings.Contains(m[0].Content, want) {
						t.Fatal(want, m[0].Content)
					}
				}
				if strings.Contains(m[1].Content, "EVALUATION_ONLY") || !strings.Contains(m[1].Content, "It's ready") || !strings.Contains(m[1].Content, "settled") {
					t.Fatal(m)
				}
				raw, _ := json.Marshal(core.PrerequisiteComparison{Result: result, Blocker: "settled", Details: "same actual condition including scope and push capability"})
				return engine.Message{Content: string(raw)}, engine.Usage{}, nil
			}
			got, err := a.comparePrerequisites(context.Background(), "Confirmed: a collaborator can publish that PR", true, settled)
			if err != nil || got.Result != result || calls != 1 {
				t.Fatal(got, calls, err)
			}
		})
	}
	// Malformed replies are bounded and cannot be taken as settlements.
	calls := 0
	a.prerequisiteComplete = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		calls++
		return engine.Message{Content: "not JSON"}, engine.Usage{}, nil
	}
	if _, err := a.comparePrerequisites(context.Background(), "Condition", false, settled); err == nil || calls != 2 {
		t.Fatal(calls, err)
	}
	a.prerequisiteComplete = func(context.Context, engine.Config, []engine.Message, []engine.Tool) (engine.Message, engine.Usage, error) {
		return engine.Message{}, engine.Usage{}, context.DeadlineExceeded
	}
	if _, err := a.comparePrerequisites(context.Background(), "Condition", false, settled); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestOwnerCoordinationReopensOnlyIdentifiedConditionAndReadTaskKeepsAnswer(t *testing.T) {
	a, p, own, _ := blockerFixture(t, false)
	ctx := context.Background()
	a.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskResearching; return "", nil })
	held, err := a.Core.RecordPlan(ctx, own.ID, core.Plan{Summary: "Build"}, nil, []string{"Library v1 tagged"})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	for _, d := range snap.Decisions {
		if d.Kind == core.DecisionPrerequisite {
			if _, err = a.Core.ChooseDecision(ctx, d.ID, core.ChoiceDropPrerequisite, core.FromOwner); err != nil {
				t.Fatal(err)
			}
		}
	}
	detail, err := blockerCall(t, a, "read_task", engine.ReadTaskArgs{ProjectID: p.ID, TaskID: own.ID})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(detail)
	if !strings.Contains(string(raw), core.ChoiceDropPrerequisite) || !strings.Contains(string(raw), held.Blockers[0].ID) {
		t.Fatal(string(raw))
	}
	snap, _ = a.Core.Snapshot(ctx)
	settledTask, _ := snap.FindTask(own.ID)
	args := engine.ReopenPrerequisiteArgs{ProjectID: p.ID, TaskID: own.ID, BlockerID: held.Blockers[0].ID, Condition: held.Blockers[0].Description, Answer: "Prerequisite " + held.Blockers[0].ID + " no longer holds: " + held.Blockers[0].Description, Settlement: settledTask.Blockers[0].CurrentSettlement}
	a.Core.EnqueueChat(ctx, "reopen", args.Answer)
	turn, startErr := a.Core.StartNextChat(ctx, "codex")
	if startErr != nil {
		t.Fatal(startErr)
	}
	ctx = core.WithOwnerInstruction(ctx, turn.ID)
	payload, _ := json.Marshal(args)
	if _, err = a.Execute(ctx, "reopen_prerequisite", payload); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Execute(ctx, "reopen_prerequisite", payload); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	pending := 0
	for _, d := range snap.Decisions {
		if d.Kind == core.DecisionPrerequisite && d.Status == core.DecisionOpen {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal(pending)
	}
}

func TestAssistantReopeningRequiresRecordedExplicitOwnerInstruction(t *testing.T) {
	for _, mode := range []string{"ambiguous", "fabricated", "unbound", "team", "wrong quote", "valid"} {
		t.Run(mode, func(t *testing.T) {
			a, p, own, _ := blockerFixture(t, false)
			ctx := context.Background()
			a.Core.UpdateTask(ctx, own.ID, func(t *core.Task, _ *core.Project) (string, error) { t.Status = core.TaskResearching; return "", nil })
			a.Core.RecordPlan(ctx, own.ID, core.Plan{Summary: "Build"}, nil, []string{"Library v1 tagged"})
			snap, _ := a.Core.Snapshot(ctx)
			for _, d := range snap.Decisions {
				if d.Kind == core.DecisionPrerequisite {
					a.Core.ChooseDecision(ctx, d.ID, core.ChoiceReadyPrerequisite, core.FromOwner)
				}
			}
			snap, _ = a.Core.Snapshot(ctx)
			current, _ := snap.FindTask(own.ID)
			b := current.Blockers[0]
			quote := "Prerequisite " + b.ID + " no longer holds: " + b.Description
			message := quote
			if mode == "ambiguous" {
				message = "Maybe it is no longer ready"
				quote = message
			}
			if mode == "fabricated" {
				message = "Please describe the report"
			}
			if mode == "wrong quote" {
				quote = "Prerequisite " + b.ID + " no longer holds: Another condition"
			}
			if mode == "team" {
				// A daemon-authored turn is not an owner message, even if it quotes one.
				a.Core.QueueChatCommand(ctx, core.CommandCompact)
				turn, _ := a.Core.StartNextChat(ctx, "codex")
				ctx = core.WithOwnerInstruction(ctx, turn.ID)
			} else {
				a.Core.EnqueueChat(ctx, "owner-instruction", message)
				turn, err := a.Core.StartNextChat(ctx, "codex")
				if err != nil {
					t.Fatal(err)
				}
				if mode != "unbound" {
					ctx = core.WithOwnerInstruction(ctx, turn.ID)
				}
			}
			args := engine.ReopenPrerequisiteArgs{ProjectID: p.ID, TaskID: own.ID, BlockerID: b.ID, Condition: b.Description, Answer: quote, Settlement: b.CurrentSettlement}
			payload, _ := json.Marshal(args)
			_, err := a.Execute(ctx, "reopen_prerequisite", payload)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, core.ErrConflict) {
				t.Fatal("untrusted action accepted", err)
			}
			snap, _ = a.Core.Snapshot(ctx)
			current, _ = snap.FindTask(own.ID)
			if (current.Blockers[0].Outcome == "") != (mode == "valid") {
				t.Fatal(current)
			}
			// The resolve_decision custom-answer route must enforce the same source.
			if mode != "valid" {
				q, err := a.Core.OpenTaskDecision(ctx, own.ID, core.DecisionQuestion, core.DecisionInput{Title: "Which format?", Context: "Format", Recommendation: "JSON", Choices: []string{"JSON", "CSV"}})
				if err != nil {
					t.Fatal(err)
				}
				a.Core.AnswerDecision(ctx, q.ID, quote, core.FromAssistant)
				snap, _ = a.Core.Snapshot(ctx)
				current, _ = snap.FindTask(own.ID)
				if current.Blockers[0].Outcome != "confirmed" {
					t.Fatal("untrusted decision answer reopened")
				}
			}
		})
	}
}
