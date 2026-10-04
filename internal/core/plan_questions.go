package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// AskPlanQuestions reconciles the saved questions immediately before opening
// their decision. Inference runs outside the transaction; the plan and examined
// prerequisite records fence the question, activity and continuation together.
func (s *Service) AskPlanQuestions(ctx context.Context, taskID string) (Task, error) {
	for attempt := 0; attempt < 3; attempt++ {
		v, err := s.Snapshot(ctx)
		if err != nil {
			return Task{}, err
		}
		t := task(&v, taskID)
		if t == nil {
			return Task{}, ErrNotFound
		}
		if t.Status != TaskResearching || t.Plan == nil || t.Plan.Answered {
			return Task{}, ErrConflict
		}
		planState, _ := json.Marshal(t.Plan)
		reconciled, err := s.reconcilePrerequisites(ctx, taskID, nil, t.Plan.Questions)
		if err != nil {
			return Task{}, err
		}
		var out Task
		err = s.store.update(ctx, func(v *Snapshot) error {
			t := task(v, taskID)
			if t == nil {
				return ErrNotFound
			}
			currentPlan, _ := json.Marshal(t.Plan)
			if string(currentPlan) != string(planState) || prerequisiteState(*t) != reconciled.state || (len(t.Revisions) > 0) != reconciled.begun {
				return errPrerequisiteStale
			}
			if t.Status != TaskResearching || t.Plan == nil || t.Plan.Answered {
				return ErrConflict
			}
			recoverPrerequisiteAnswers(v, t)
			now := s.now().UTC()
			t.Plan.Questions = reconciled.questions
			t.Plan.Prerequisites = taskPrerequisites(*t)
			recordPrerequisiteMatches(v, t, reconciled.matches, now)
			if len(t.Plan.Questions) > 0 {
				title := fmt.Sprintf("%s has questions about “%s”", t.Plan.Role, t.Objective)
				if len(t.Revisions) == 0 {
					title += " before starting"
				}
				var questions strings.Builder
				for i, question := range t.Plan.Questions {
					fmt.Fprintf(&questions, "%d. %s\n", i+1, question)
				}
				d := openTaskDecision(v, t, DecisionQuestion, DecisionInput{Title: title, Context: strings.TrimSpace(questions.String()), Recommendation: "Answer what you can, or let the team use its judgment", Choices: []string{"Use your judgment", "Stop"}}, now)
				t.Asker = &Asker{Decision: d.ID, From: t.Plan.Role, Step: TaskResearching}
			} else if heldBack(v, *t) && len(t.Revisions) == 0 {
				t.Status, t.Plan, t.Detail = TaskQueued, nil, ""
				t.Base, t.From, t.Branch = "", "", ""
			} else if request := t.OpenResearch(); request != nil {
				request.Researcher, request.AnsweredAt = t.Plan.Role, now
				t.reopen(request.From, request.Revision)
				t.Status, t.Detail = TaskReviewing, "Back from "+t.Plan.Role+" with more research"
				recordTask(v, now, t, "task.researched", fmt.Sprintf("%s researched %s again for %s", t.Plan.Role, t.Objective, request.From))
			} else {
				t.Status, t.Detail = TaskWriting, ""
			}
			t.UpdatedAt = now
			derive(v, t)
			out = *t
			return nil
		})
		if errors.Is(err, errPrerequisiteStale) {
			continue
		}
		return out, err
	}
	return Task{}, fmt.Errorf("prerequisites or plan changed before asking questions: %w", ErrConflict)
}
