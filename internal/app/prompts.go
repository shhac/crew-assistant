package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

func briefText(p core.Project, t core.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\nGoal: %s\n", p.Title, p.Brief.Goal)
	if p.Brief.Audience != "" {
		fmt.Fprintf(&b, "Audience: %s\n", p.Brief.Audience)
	}
	if p.Brief.Constraints != "" {
		fmt.Fprintf(&b, "Constraints: %s\n", p.Brief.Constraints)
	}
	fmt.Fprintf(&b, "\nThis task: %s\n", t.Objective)
	criteria := append(append([]string{}, p.Brief.Criteria...), t.Criteria...)
	if len(criteria) > 0 {
		b.WriteString("\nThe result must meet every one of these criteria:\n")
		for i, c := range criteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, c)
		}
	}
	if len(t.Direction) > 0 {
		b.WriteString("\nThe owner has also said:\n")
		for _, d := range t.Direction {
			fmt.Fprintf(&b, "- %s\n", d)
		}
	}
	return b.String()
}

// writerPrompt stands on its own, so a fresh session can pick the work up if
// the previous one cannot be resumed.
func writerPrompt(p core.Project, t core.Task) string {
	var b strings.Builder
	b.WriteString(briefText(p, t))
	last := len(t.Revisions)
	if last == 0 {
		b.WriteString("\nWrite the deliverable as one or more files in the current working directory. Markdown is preferred for prose.\n")
	} else {
		fmt.Fprintf(&b, "\nThe working directory holds draft %d. Revise it in place. The reviewers said:\n", last)
		for _, v := range t.Verdicts {
			if v.Revision != last || v.Outcome == core.VerdictPass {
				continue
			}
			fmt.Fprintf(&b, "\n%s: %s\n", v.Role, v.Summary)
			for _, f := range v.Findings {
				if f.Criterion != "" {
					fmt.Fprintf(&b, "- [%s] %s\n", f.Criterion, f.Note)
				} else {
					fmt.Fprintf(&b, "- %s\n", f.Note)
				}
			}
		}
		if len(p.Brief.Criteria) > 0 && t.Revisions[last-1].BriefVersion != p.Brief.Version {
			b.WriteString("\nThe brief has changed since that draft. Make sure the revision meets the brief above.\n")
		}
	}
	b.WriteString("\nOnly change files in the working directory. Do not send, publish or deliver anything anywhere; the owner approves delivery.\nEnd your reply with two sentences on what you wrote or changed.")
	return b.String()
}

func reviewerPrompt(p core.Project, t core.Task, r core.Revision) string {
	var b strings.Builder
	b.WriteString(briefText(p, t))
	fmt.Fprintf(&b, "\nThe current directory holds draft %d: %s.\nRead every file. Do not modify anything.\n", r.N, strings.Join(r.Files, ", "))
	b.WriteString(`
Judge the draft strictly against the goal, audience, constraints and every criterion above. Use:
- "pass" only when every criterion is met and nothing important is wrong;
- "revise" when something should change, with one finding per issue, naming the criterion it concerns;
- "question" only when the brief is genuinely ambiguous and you cannot judge without the owner.

Reply with only this JSON object:
{"outcome": "pass" | "revise" | "question", "summary": "one or two sentences", "findings": [{"criterion": "...", "note": "..."}], "question": "only for outcome question, else empty"}`)
	return b.String()
}

// parseVerdict reads the reviewer's JSON answer, tolerating a fenced block or
// surrounding prose, and rejects anything that is not a usable verdict.
func parseVerdict(text string) (core.Verdict, error) {
	body := text
	if i := strings.LastIndex(body, "```json"); i >= 0 {
		body = body[i+len("```json"):]
		if j := strings.Index(body, "```"); j >= 0 {
			body = body[:j]
		}
	} else if start, end := strings.Index(body, "{"), strings.LastIndex(body, "}"); start >= 0 && end > start {
		body = body[start : end+1]
	}
	var v struct {
		Outcome  string         `json:"outcome"`
		Summary  string         `json:"summary"`
		Findings []core.Finding `json:"findings"`
		Question string         `json:"question"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &v); err != nil {
		return core.Verdict{}, errors.New("the review was not valid JSON")
	}
	switch v.Outcome {
	case core.VerdictPass, core.VerdictRevise:
	case core.VerdictQuestion:
		if strings.TrimSpace(v.Question) == "" {
			return core.Verdict{}, errors.New("the review asked a question without asking it")
		}
	default:
		return core.Verdict{}, fmt.Errorf("the review had no usable outcome %q", v.Outcome)
	}
	if strings.TrimSpace(v.Summary) == "" {
		return core.Verdict{}, errors.New("the review had no summary")
	}
	if v.Outcome == core.VerdictRevise && len(v.Findings) == 0 {
		return core.Verdict{}, errors.New("the review asked for changes without naming any")
	}
	return core.Verdict{Outcome: v.Outcome, Summary: strings.TrimSpace(v.Summary), Findings: v.Findings, Question: strings.TrimSpace(v.Question)}, nil
}

func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "…"
}
