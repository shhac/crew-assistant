package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// managePM gives the team's PM one look at a project's to-do list after
// something changed it: work queued, planned or finished. The PM sets the
// order work starts in and what waits for what; it directs no one, and the
// owner's or the assistant's order stands over it.
func (lp *Loop) managePM(ctx context.Context, snap core.Snapshot) (bool, error) {
	for _, p := range snap.Projects {
		if !p.PMDue || pmAsking(snap, p.ID) {
			continue
		}
		seat, ok := p.PMSeat()
		if !ok {
			return true, lp.Core.SkipPM(ctx, p.ID, "")
		}
		if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
			continue
		}
		return true, lp.pmTurn(ctx, snap, p, seat)
	}
	return false, nil
}

// pmAsking says whether the PM is waiting on the owner's answer, which
// brings it back anyway; looking again before then would only ask again.
func pmAsking(snap core.Snapshot, projectID string) bool {
	return slices.ContainsFunc(snap.Decisions, func(d core.Decision) bool {
		return d.ProjectID == projectID && d.Kind == core.DecisionPMQuestion && d.Status == core.DecisionOpen
	})
}

func (lp *Loop) pmTurn(ctx context.Context, snap core.Snapshot, p core.Project, seat core.Role) error {
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := pmPrompt(snap, p)
	// The PM reads only what its prompt carries: no repository, no writing.
	spec := lp.baseSpec(seat, dir, base)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return lp.Core.SkipPM(ctx, p.ID, text.Clip(err.Error(), 300))
		}
		answer, questions, err := parsePM(result.Text)
		if err != nil {
			spec.Prompt = retryPrompt(base, err)
			continue
		}
		if _, err := lp.Core.ApplyPM(ctx, p.ID, answer); err != nil {
			return err
		}
		return lp.askPMQuestions(ctx, p, seat, questions)
	}
	return lp.Core.SkipPM(ctx, p.ID, "its reply could not be read")
}

// askPMQuestions brings what the PM couldn't settle about the order to the
// owner.
func (lp *Loop) askPMQuestions(ctx context.Context, p core.Project, seat core.Role, questions []string) error {
	if len(questions) == 0 {
		return nil
	}
	_, err := lp.Core.AskForPM(ctx, p.ID, core.DecisionInput{
		Title:          fmt.Sprintf("%s has questions about the order of work in %s", seat.Name, p.Title),
		Context:        strings.TrimSpace(numbered(questions)),
		Recommendation: "Answer, or let the PM use its judgment",
		Choices:        []string{"Use your judgment", "Keep the order as it is"},
	})
	return err
}

// pmPrompt is everything the PM needs to order the list: the brief, every
// unfinished task with its plan and what it waits for, and who set the order.
func pmPrompt(snap core.Snapshot, p core.Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the to-do list for the project %s. Goal: %s\n", p.Title, p.Brief.Goal)
	b.WriteString(`
Decide the order the queued tasks start in, and what each unfinished task has to wait for. A task waits for another when it builds on what the other will change; without stacking, a task never starts before what it waits for has landed. Put first what unblocks the most, then what the owner most needs. Do not plan or build anything yourself, and do not direct the team; only order the list.
`)
	switch p.OrderedBy {
	case core.OrderedByOwner, core.OrderedByAssistant:
		fmt.Fprintf(&b, "\nThe %s set the current order; keep it for the tasks that were there then, and only place tasks queued since.\n", p.OrderedBy)
	}
	if p.PMDirection != "" {
		fmt.Fprintf(&b, "\nThe owner told you: %s\n", p.PMDirection)
	}
	pmTasks(&b, snap, p)
	b.WriteString(`
Reply with only this JSON object:
{"order": ["every queued task id, in the order they should start"], "depends": [{"task": "id", "on": ["ids it must wait for; the full list, replacing what it has"]}], "note": "one line on what you changed and why", "questions": ["only what the owner must decide"]}`)
	return b.String()
}

// pmTasks is the list as the PM keeps it: every unfinished task, queued ones
// in their current order, with its plan and what it waits for.
func pmTasks(b *strings.Builder, snap core.Snapshot, p core.Project) {
	b.WriteString("\nUnfinished tasks, queued ones in their current order:\n")
	for _, t := range snap.Tasks {
		if t.ProjectID != p.ID || t.Finished() {
			continue
		}
		fmt.Fprintf(b, "- %s (%s): %s\n", t.ID, t.Status, text.Clip(t.Objective, 300))
		if t.Plan != nil {
			fmt.Fprintf(b, "  plan: %s\n", text.Clip(t.Plan.Summary, 400))
			if len(t.Plan.Changes) > 0 {
				fmt.Fprintf(b, "  changes: %s\n", text.Clip(strings.Join(t.Plan.Changes, "; "), 400))
			}
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(b, "  waits for: %s\n", strings.Join(t.DependsOn, ", "))
		}
	}
}

// AskPM puts the assistant's question to a project's PM and gives its
// answer. The PM answers from what it keeps, the list with each task's plan
// and what waits for what, so the assistant needn't carry any of it; asked
// this way, it changes nothing.
func (lp *Loop) AskPM(ctx context.Context, projectID, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("ask the PM something")
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return "", core.ErrNotFound
	}
	seat, ok := p.PMSeat()
	if !ok {
		return "", fmt.Errorf("%s has no PM; read_task looks at a task directly: %w", p.Title, core.ErrConflict)
	}
	if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
		return "", fmt.Errorf("%s is holding back for its usage allowance until %s: %w", seat.Name, wait.Format("15:04"), core.ErrConflict)
	}
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the to-do list for the project %s. Goal: %s\n", p.Title, p.Brief.Goal)
	pmTasks(&b, snap, p)
	fmt.Fprintf(&b, "\nThe owner's assistant asks you:\n\n%s\n\nAnswer in a few plain sentences from what you know of the list. You change nothing by answering; say what you would change, if anything, and why.", question)
	result, err := lp.runner.Run(ctx, lp.baseSpec(seat, dir, b.String()))
	if err != nil {
		return "", err
	}
	answer := text.Clip(strings.TrimSpace(result.Text), 4000)
	if answer == "" {
		return "", fmt.Errorf("%s gave no answer", seat.Name)
	}
	_ = lp.Core.RecordActivity(ctx, p.ID, "pm.asked", fmt.Sprintf("The assistant asked %s: %s. %s", seat.Name, text.Clip(question, 200), text.Clip(answer, 300)))
	return answer, nil
}

// parsePM reads the PM's JSON answer, tolerating a fenced block or prose.
func parsePM(reply string) (core.PMAnswer, []string, error) {
	var in struct {
		Order   []string `json:"order"`
		Depends []struct {
			Task string   `json:"task"`
			On   []string `json:"on"`
		} `json:"depends"`
		Note      string   `json:"note"`
		Questions []string `json:"questions"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.PMAnswer{}, nil, errors.New("the reply was not valid JSON")
	}
	answer := core.PMAnswer{Order: in.Order, Depends: map[string][]string{}, Note: text.Clip(strings.TrimSpace(in.Note), 300)}
	for _, d := range in.Depends {
		answer.Depends[strings.TrimSpace(d.Task)] = d.On
	}
	return answer, listed(in.Questions, 5), nil
}
