package work

import (
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func linkedIssue(identifier string) core.LinearRef {
	return core.LinearRef{Kind: "issue", LinearIssue: core.LinearIssue{ID: identifier + "-id", Identifier: identifier}}
}

// A pull request names each of its task's Linear issues with a magic word,
// unless its text already does, and says how each of its drafts was written.
func TestPullRequestDescriptionReferencesLinearAndProvenance(t *testing.T) {
	t.Parallel()
	task := core.Task{
		Roles: []core.Role{
			{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Model: "Claude-Opus-4-5", Effort: "high"},
			{Name: "Bo", Kinds: []string{core.RoleImplementer}, Engine: "codex"},
			{Name: "Cy", Kinds: []string{core.RoleImplementer}, Engine: "grok", Model: "grok 4"},
		},
		Revisions:   []core.Revision{{N: 1, Seat: "Ada"}, {N: 2, Seat: "Ada"}, {N: 3, By: core.DraftByOwner}, {N: 4, Seat: "Bo"}, {N: 5, Seat: "Bo", CleanMergeOf: 4}, {N: 6, Seat: "Gone"}},
		Linear:      []core.LinearRef{linkedIssue("EX-1454")},
		LinearLinks: []core.LinearRef{linkedIssue("EX-12"), linkedIssue("EX-7"), {Kind: "project", LinearIssue: core.LinearIssue{ID: "p", Identifier: "Roadmap"}}},
	}
	body := description(task, core.PRText{Title: "Renew", Body: "Renews memberships.\n\nPart of EX-7. See EX-123."})
	want := []string{
		"Renews memberships.\n\nPart of EX-7. See EX-123.\n\nFixes EX-1454\nFixes EX-12\n\n" + prFooter,
		"<!-- agent-provenance v=1 harness=claude-code model=claude-opus-4-5 effort=high -->",
		"<!-- agent-provenance v=1 harness=codex model=unknown effort=unknown -->",
		"<!-- agent-provenance v=1 harness=unknown model=unknown effort=unknown -->",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in:\n%s", w, body)
		}
	}
	if strings.Count(body, "agent-provenance") != 3 || strings.Contains(body, "Fixes EX-7") || strings.Contains(body, "Roadmap") || !strings.HasSuffix(body, "-->") {
		t.Fatalf("body:\n%s", body)
	}
	task.Revisions = append(task.Revisions, core.Revision{N: 7, Seat: "Cy"})
	if grok := description(task, core.PRText{Body: "x"}); !strings.Contains(grok, "harness=grok model=unknown effort=unknown") {
		t.Fatalf("a model that can't be written as one word was given: %s", grok)
	}

	existing := "<!-- agent-provenance v=1 harness=claude-code model=claude-opus-4-5 effort=high -->"
	again := description(task, core.PRText{Body: "Fixes ex-1454, closes EX-12.\n" + existing + "\n<!-- agent-provenance v=1 harness=other model=x effort=y -->"})
	if strings.Count(again, existing) != 1 || strings.Contains(again, "Fixes EX-1454") || strings.Contains(again, "Fixes EX-12") || !strings.Contains(again, "Fixes EX-7") || !strings.Contains(again, "harness=other model=x effort=y") {
		t.Fatalf("existing references or provenance rewritten:\n%s", again)
	}

	// A link made after the pull request opened changes what it's given, so
	// its description is updated to match.
	plain := core.Task{}
	before := described("Renew", description(plain, core.PRText{Body: "Renews."}))
	plain.LinearLinks = []core.LinearRef{linkedIssue("EX-9")}
	if described("Renew", description(plain, core.PRText{Body: "Renews."})) == before {
		t.Fatal("a new link left the description as it was")
	}
}
