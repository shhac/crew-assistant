package work

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// reviewedTask is a task on its fifth draft, each earlier one sent back.
func reviewedTask() core.Task {
	task := core.Task{Objective: "Add Feature", Base: "abc"}
	for n := 1; n <= 5; n++ {
		task.Revisions = append(task.Revisions, core.Revision{N: n, Summary: "I fixed everything in draft " + fmt.Sprint(n)})
	}
	for n := 1; n <= 4; n++ {
		task.Verdicts = append(task.Verdicts, core.Verdict{Revision: n, Role: "Reviewer", Outcome: core.VerdictRevise, Summary: "Not yet.",
			Findings: []core.Finding{{Criterion: "Handles errors", Note: "Gap " + fmt.Sprint(n)}}})
	}
	task.Verdicts = append(task.Verdicts,
		core.Verdict{Revision: 4, Role: "QA", Outcome: core.VerdictRevise, Summary: "Fails.", Findings: []core.Finding{{Note: "The vet warning"}}},
		core.Verdict{Revision: 5, Role: "QA", Outcome: core.VerdictRevise, Summary: "Fails.", Findings: []core.Finding{{Note: "Judged beside you"}}},
	)
	return task
}

// A reviewer starts afresh every draft, so it is shown what the checks found
// on the latest few drafts, and never the implementer's account of them.
func TestAReviewerIsShownTheEarlierFindingsAndAskedForEverything(t *testing.T) {
	code := core.Project{Brief: core.Brief{Goal: "Add features"}, Playbook: &core.Playbook{Medium: core.MediumGit}}
	docs := core.Project{Brief: core.Brief{Goal: "Write notes"}}
	task := reviewedTask()
	rev := task.Revisions[4]
	reviewer := core.Role{Name: "Reviewer", Kinds: []string{core.RoleReviewer}}
	prompts := map[string]string{
		"code reviewer": checkerPrompt(code, task, rev, reviewer, code.Playbook),
		"reviewer":      reviewerPrompt(docs, task, rev),
	}
	for name, prompt := range prompts {
		for _, want := range []string{
			"What the checks found on earlier drafts of this task:\n- Draft 2, Reviewer: [Handles errors] Gap 2\n- Draft 3, Reviewer: [Handles errors] Gap 3\n- Draft 4, Reviewer: [Handles errors] Gap 4\n- Draft 4, QA: The vet warning\n",
			`Start by checking each earlier finding above against this draft: say in your summary which are fixed, and keep each one that is not as a finding marked "still open from draft N". Then review the whole`,
			`This may be the last review: report every issue you find now, in one verdict, each tied to a criterion or plan item; mark minor ones "follow-up".`,
		} {
			if !strings.Contains(prompt, want) {
				t.Fatalf("the %s prompt lacks %q: %s", name, want, prompt)
			}
		}
		for _, unwanted := range []string{"Gap 1", "Judged beside you", "I fixed everything"} {
			if strings.Contains(prompt, unwanted) {
				t.Fatalf("the %s prompt should not carry %q: %s", name, unwanted, prompt)
			}
		}
	}
	walk := "Before replying, walk these paths through the change: stopping mid-step, a crash between two writes, concurrent callers, partial failure, state a model turn changed, and every other path that applies the same rule."
	if !strings.Contains(prompts["code reviewer"], walk) || strings.Contains(prompts["reviewer"], walk) {
		t.Fatalf("only a code review walks the failure paths: %s", prompts["reviewer"])
	}
	// A first draft has nothing earlier to check, but is still reviewed in full.
	first := checkerPrompt(code, task, task.Revisions[0], reviewer, code.Playbook)
	if strings.Contains(first, "earlier finding") || strings.Contains(first, "What the checks found") || !strings.Contains(first, "This may be the last review") {
		t.Fatalf("a first draft's review: %s", first)
	}
	// QA runs its check and reads the output; the findings are not its to settle.
	qa := checkerPrompt(code, task, rev, core.Role{Name: "QA", Kinds: []string{core.RoleQA}}, &core.Playbook{Medium: core.MediumGit, Check: "make check"})
	if strings.Contains(qa, "Gap 4") || strings.Contains(qa, "This may be the last review") {
		t.Fatalf("QA's check should be as it was: %s", qa)
	}
}
