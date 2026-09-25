package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
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
	spec := roles.Spec{Engine: seat.Engine, Model: seat.Model, Effort: seat.Effort, WorkDir: dir, Instructions: seat.Instructions, Prompt: base}
	lp.engine(&spec, seat, lp.Config())
	for attempt := 0; attempt < 2; attempt++ {
		result, err := lp.runner.Run(ctx, spec)
		if err != nil {
			return lp.Core.SkipPM(ctx, p.ID, text.Clip(err.Error(), 300))
		}
		answer, questions, err := parsePM(result.Text)
		if err != nil {
			spec.Prompt = base + "\n\nYour previous reply could not be used (" + err.Error() + "). Reply with only the JSON object."
			continue
		}
		if _, err := lp.Core.ApplyPM(ctx, p.ID, answer); err != nil {
			return err
		}
		if len(questions) == 0 {
			return nil
		}
		var b strings.Builder
		for i, q := range questions {
			fmt.Fprintf(&b, "%d. %s\n", i+1, q)
		}
		_, err = lp.Core.CreateDecision(ctx, core.DecisionInput{
			ProjectID:      p.ID,
			Kind:           core.DecisionPMQuestion,
			Title:          fmt.Sprintf("%s has questions about the order of work in %s", seat.Name, p.Title),
			Context:        strings.TrimSpace(b.String()),
			Recommendation: "Answer, or let the PM use its judgment",
			Choices:        []string{"Use your judgment", "Keep the order as it is"},
		})
		return err
	}
	return lp.Core.SkipPM(ctx, p.ID, "its reply could not be read")
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
	b.WriteString("\nUnfinished tasks, queued ones in their current order:\n")
	for _, t := range snap.Tasks {
		if t.ProjectID != p.ID || t.Finished() {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", t.ID, t.Status, text.Clip(t.Objective, 300))
		if t.Plan != nil {
			fmt.Fprintf(&b, "  plan: %s\n", text.Clip(t.Plan.Summary, 400))
			if len(t.Plan.Changes) > 0 {
				fmt.Fprintf(&b, "  changes: %s\n", text.Clip(strings.Join(t.Plan.Changes, "; "), 400))
			}
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(&b, "  waits for: %s\n", strings.Join(t.DependsOn, ", "))
		}
	}
	b.WriteString(`
Reply with only this JSON object:
{"order": ["every queued task id, in the order they should start"], "depends": [{"task": "id", "on": ["ids it must wait for; the full list, replacing what it has"]}], "note": "one line on what you changed and why", "questions": ["only what the owner must decide"]}`)
	return b.String()
}

// parsePM reads the PM's JSON answer, tolerating a fenced block or prose.
func parsePM(reply string) (core.PMAnswer, []string, error) {
	body := reply
	if i := strings.LastIndex(body, "```json"); i >= 0 {
		body = body[i+len("```json"):]
		if j := strings.Index(body, "```"); j >= 0 {
			body = body[:j]
		}
	} else if start, end := strings.Index(body, "{"), strings.LastIndex(body, "}"); start >= 0 && end > start {
		body = body[start : end+1]
	}
	var in struct {
		Order   []string `json:"order"`
		Depends []struct {
			Task string   `json:"task"`
			On   []string `json:"on"`
		} `json:"depends"`
		Note      string   `json:"note"`
		Questions []string `json:"questions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &in); err != nil {
		return core.PMAnswer{}, nil, errors.New("the reply was not valid JSON")
	}
	answer := core.PMAnswer{Order: in.Order, Depends: map[string][]string{}, Note: text.Clip(strings.TrimSpace(in.Note), 300)}
	for _, d := range in.Depends {
		answer.Depends[strings.TrimSpace(d.Task)] = d.On
	}
	var questions []string
	for _, q := range in.Questions {
		if q = strings.TrimSpace(q); q != "" && len(questions) < 5 {
			questions = append(questions, text.Clip(q, 500))
		}
	}
	return answer, questions, nil
}
