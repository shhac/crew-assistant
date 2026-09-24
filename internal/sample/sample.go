// Package sample is the fictional sample demo mode starts with: a few projects
// with work at every stage, so the dashboard can be seen doing its job
// without a model or a real repository.
package sample

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/localdocs"
)

// Seed fills an empty store with the sample. Sample folders are made under
// dir, which demo mode removes on exit.
func Seed(ctx context.Context, s *core.Service, dir string) error {
	now := time.Now().UTC()
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	sample := build(dir, ago)
	for _, p := range sample.Projects {
		for _, folder := range p.Directories {
			if err := os.MkdirAll(folder, 0700); err != nil {
				return err
			}
		}
	}
	seeded, err := s.SeedDemo(ctx, sample)
	if err != nil {
		return err
	}
	return writeDrafts(seeded)
}

var (
	codeTeam = []core.Role{
		{Name: "Implementer", Kind: core.RoleImplementer, Engine: "claude"},
		{Name: "Reviewer", Kind: core.RoleReviewer, Engine: "codex"},
		{Name: "QA", Kind: core.RoleQA, Engine: "claude"},
	}
	// The crew-assistant project is staffed by two of the owner's members.
	crewTeam = []core.Role{
		{Name: "Ada", Kind: core.RoleImplementer, Engine: "claude", Model: "opus", Member: "demo-ada"},
		{Name: "Rune", Kind: core.RoleReviewer, Engine: "codex", Member: "demo-rune"},
		{Name: "QA", Kind: core.RoleQA, Engine: "claude"},
	}
	writingTeam = []core.Role{
		{Name: "Writer", Kind: core.RoleImplementer, Engine: "claude"},
		{Name: "Reviewer", Kind: core.RoleReviewer, Engine: "codex"},
	}
)

func codePlaybook(repo string, land core.LandPolicy, team []core.Role) *core.Playbook {
	return &core.Playbook{Template: "code", Medium: core.MediumGit, Roles: team, MaxRounds: 3, Deliver: "owner", Repo: repo, BranchPrefix: "crew/", Check: "make check", Land: land}
}

func build(dir string, ago func(time.Duration) time.Time) core.Snapshot {
	crewRepo := filepath.Join(dir, "projects", "crew-assistant")
	docsRepo := filepath.Join(dir, "projects", "docs-site")
	fastForward := core.LandPolicy{Via: core.LandPush, Target: "main", Method: "fast-forward", Approve: core.ApproveBefore, Means: "the next release includes it"}
	pullRequest := core.LandPolicy{Via: core.LandPullRequest, Target: "main", Method: "squash", GitHub: "example/docs-site", Approve: core.ApproveBefore}
	crew := core.Project{ID: "demo-crew", Title: "crew-assistant", Status: "active", Directories: []string{crewRepo}, Playbook: codePlaybook(crewRepo, fastForward, crewTeam), UpdatedAt: ago(4 * time.Minute),
		Brief: core.Brief{Version: 3, Goal: "Make crew-assistant a software factory that can build and improve itself.", Criteria: []string{"Features land on main without losing work", "Tests never touch real services"}, UpdatedAt: ago(72 * time.Hour)}}
	docs := core.Project{ID: "demo-docs", Title: "docs-site", Status: "active", Directories: []string{docsRepo}, Playbook: codePlaybook(docsRepo, pullRequest, codeTeam), UpdatedAt: ago(22 * time.Minute),
		Brief: core.Brief{Version: 1, Goal: "A documentation site people can find answers in quickly.", Audience: "Developers new to the product", Criteria: []string{"Every page loads fast", "Search finds pages by their headings"}, UpdatedAt: ago(240 * time.Hour)}}
	memo := core.Project{ID: "demo-memo", Title: "Q4 planning memo", Status: "active", UpdatedAt: ago(time.Minute),
		Playbook: &core.Playbook{Template: "draft", Medium: core.MediumDocuments, Roles: writingTeam, MaxRounds: 3, Deliver: "owner"},
		Brief:    core.Brief{Version: 1, Goal: "A one-page plan for Q4 that the team can agree on in one meeting.", Audience: "The product team", Constraints: "One page. Plain language.", Criteria: []string{"Three priorities at most", "Each priority says how we will know it worked"}, UpdatedAt: ago(48 * time.Hour)}}
	reading := core.Project{ID: "demo-reading", Title: "Reading list", Status: "active", UpdatedAt: ago(72 * time.Hour),
		Brief: core.Brief{Version: 1, Goal: "Keep track of what to read next.", Criteria: []string{}, UpdatedAt: ago(72 * time.Hour)}}

	started := func(p core.Project, id, objective string, status string, round int, created time.Time) core.Task {
		return core.Task{ID: id, ProjectID: p.ID, Objective: objective, Status: status, Roles: p.Playbook.Roles, Playbook: p.Playbook, MaxRounds: 3, Round: round, Revisions: []core.Revision{}, Verdicts: []core.Verdict{}, CreatedAt: created, UpdatedAt: created}
	}
	queued := func(p core.Project, id, objective string, created time.Time) core.Task {
		return core.Task{ID: id, ProjectID: p.ID, Objective: objective, Status: core.TaskQueued, Revisions: []core.Revision{}, Verdicts: []core.Verdict{}, CreatedAt: created, UpdatedAt: created}
	}
	pass := func(n int, role, summary string, at time.Time) core.Verdict {
		return core.Verdict{Revision: n, Role: role, BriefVersion: 3, Outcome: core.VerdictPass, Summary: summary, At: at}
	}

	signing := started(crew, "demo-signing", "Sign commits as your git config says", core.TaskWaiting, 2, ago(3*time.Hour))
	signing.Branch, signing.Base = "crew/sign-commits", "88f0043"
	signing.Revisions = []core.Revision{
		{N: 1, BriefVersion: 3, Ref: "5d1e2a0", Files: []string{"internal/media/gitrepo/sign.go", "internal/media/gitrepo/gitrepo.go"}, Summary: "Commits in the clone now sign as the owner's git config says. A project can override it.", At: ago(150 * time.Minute)},
		{N: 2, BriefVersion: 3, Ref: "b30f381", Files: []string{"internal/media/gitrepo/sign.go", "internal/media/gitrepo/gitrepo.go", "internal/media/gitrepo/gitrepo_test.go", "internal/core/playbook.go"}, Summary: "Signing failures now say signing was the cause, and the tests use a stand-in signer.", At: ago(40 * time.Minute)},
	}
	signing.Verdicts = []core.Verdict{
		{Revision: 1, Role: "Rune", BriefVersion: 3, Outcome: core.VerdictRevise, Summary: "Close, but a failed signature gives no hint why.", Findings: []core.Finding{{Note: "Say in the error that signing failed and how to turn it off."}, {Note: "Test with a stand-in gpg program, not the real one."}}, At: ago(130 * time.Minute)},
		pass(1, "QA", "make check passed.", ago(120*time.Minute)),
		pass(2, "Rune", "Both findings resolved, none new.", ago(20*time.Minute)),
		pass(2, "QA", "make check passed in 3 min 12 s.", ago(6*time.Minute)),
	}
	signing.DecisionID = "demo-land-signing"

	grouping := started(crew, "demo-grouping", "Group the Projects list by status", core.TaskReviewing, 2, ago(90*time.Minute))
	grouping.Revisions = []core.Revision{
		{N: 1, BriefVersion: 3, Ref: "a41c9e2", Files: []string{"internal/dashboard/ui/src/ProjectsPage.tsx"}, Summary: "Projects are grouped by what they need from you.", At: ago(70 * time.Minute)},
		{N: 2, BriefVersion: 3, Ref: "c07d3b8", Files: []string{"internal/dashboard/ui/src/ProjectsPage.tsx", "internal/dashboard/ui/src/ProjectsPage.test.tsx"}, Summary: "Added tests for each group and an empty state.", At: ago(12 * time.Minute)},
	}
	grouping.Verdicts = []core.Verdict{
		{Revision: 1, Role: "Rune", BriefVersion: 3, Outcome: core.VerdictRevise, Summary: "Needs tests for the grouping.", Findings: []core.Finding{{Note: "No test covers a project with no requests."}}, At: ago(60 * time.Minute)},
		pass(2, "Rune", "Tests cover every group now.", ago(4*time.Minute)),
	}
	grouping.Messages = []core.TeamMessage{{ID: "demo-message-qa", To: "QA", Kind: core.RoleQA, From: core.FromOwner, Text: "Run it with the race detector too, please.", Status: core.MessageWorking, At: ago(3 * time.Minute)}}

	search := started(crew, "demo-search", "Search across projects", core.TaskWriting, 1, ago(25*time.Minute))
	search.Detail = "Writing the first draft"

	landed := func(id, objective, commit string, at time.Time) core.Task {
		t := started(crew, id, objective, core.TaskLanded, 1, at.Add(-2*time.Hour))
		t.Revisions = []core.Revision{{N: 1, BriefVersion: 3, Ref: commit, Summary: "Done.", At: at.Add(-time.Hour)}}
		t.Verdicts = []core.Verdict{pass(1, "Rune", "Good to go.", at.Add(-50*time.Minute)), pass(1, "QA", "make check passed.", at.Add(-40*time.Minute))}
		t.Approved, t.DeliveredTo, t.Detail, t.UpdatedAt = 1, "main", "Landed on main", at
		return t
	}

	darkDocs := started(docs, "demo-dark-docs", "Dark mode for the docs", core.TaskAwaiting, 1, ago(26*time.Hour))
	darkDocs.Revisions = []core.Revision{{N: 1, BriefVersion: 1, Ref: "e91a4f3", Files: []string{"src/theme.css", "src/layout.tsx"}, Summary: "A dark theme that follows the system setting.", At: ago(5 * time.Hour)}}
	darkDocs.Verdicts = []core.Verdict{
		{Revision: 1, Role: "Reviewer", BriefVersion: 1, Outcome: core.VerdictPass, Summary: "Contrast checked on every page.", At: ago(4 * time.Hour)},
		{Revision: 1, Role: "QA", BriefVersion: 1, Outcome: core.VerdictPass, Summary: "make check passed.", At: ago(4 * time.Hour)},
	}
	darkDocs.Approved = 1
	darkDocs.Proposal = &core.Proposal{Branch: "crew/dark-mode", Number: 128, URL: "https://github.com/example/docs-site/pull/128"}

	changelog := started(docs, "demo-changelog", "Changelog page", core.TaskWaiting, 1, ago(3*time.Hour))
	changelog.Revisions = []core.Revision{{N: 1, BriefVersion: 1, Ref: "7b2c1d4", Files: []string{"src/pages/changelog.tsx"}, Summary: "A changelog page built from the release notes.", At: ago(40 * time.Minute)}}
	changelog.Verdicts = []core.Verdict{{Revision: 1, Role: "Reviewer", BriefVersion: 1, Outcome: core.VerdictQuestion, Summary: "The brief doesn't say which releases to list.", Question: "Should the changelog list patch releases, or only minor ones with their patches linked?", At: ago(22 * time.Minute)}}
	changelog.DecisionID = "demo-changelog-question"

	searchPage := started(docs, "demo-search-page", "Search results page", core.TaskReviewing, 3, ago(8*time.Hour))
	searchPage.Revisions = []core.Revision{{N: 1, BriefVersion: 1, Ref: "1f0e9a2", Summary: "First results page.", At: ago(6 * time.Hour)}, {N: 2, BriefVersion: 1, Ref: "3c8d7b1", Summary: "Highlights matches.", At: ago(3 * time.Hour)}, {N: 3, BriefVersion: 1, Ref: "9a4e2c6", Summary: "Keyboard navigation between results.", At: ago(5 * time.Minute)}}

	plan := started(memo, "demo-plan", "One-page Q4 plan", core.TaskWaiting, 2, ago(5*time.Hour))
	plan.Revisions = []core.Revision{
		{N: 1, BriefVersion: 1, Files: []string{"q4-plan.md"}, Summary: "A first plan with four priorities.", At: ago(4 * time.Hour)},
		{N: 2, BriefVersion: 1, Files: []string{"q4-plan.md"}, Summary: "Cut to three priorities, each with how we will know it worked.", At: ago(30 * time.Minute)},
	}
	plan.Verdicts = []core.Verdict{
		{Revision: 1, Role: "Reviewer", BriefVersion: 1, Outcome: core.VerdictRevise, Summary: "Four priorities is one too many.", Findings: []core.Finding{{Criterion: "Three priorities at most", Note: "Merge onboarding into activation."}}, At: ago(3 * time.Hour)},
		{Revision: 2, Role: "Reviewer", BriefVersion: 1, Outcome: core.VerdictPass, Summary: "Three clear priorities, each measurable.", At: ago(10 * time.Minute)},
	}
	plan.DecisionID = "demo-approve-plan"

	tasks := []core.Task{
		signing, grouping, search,
		queued(crew, "demo-shortcuts", "Keyboard shortcuts for the board", ago(50*time.Minute)),
		queued(crew, "demo-screenshots", "Light and dark screenshots in the README", ago(45*time.Minute)),
		landed("demo-assets", "Composer asset drop and paste", "ce5899e", ago(3*time.Hour)),
		landed("demo-suggestions", "Next-message suggestions", "482bf96", ago(150*time.Minute)),
		darkDocs, changelog, searchPage, plan,
		queued(memo, "demo-notes", "Speaker notes for the plan", ago(20*time.Minute)),
	}
	decisions := []core.Decision{
		{ID: "demo-land-signing", ProjectID: crew.ID, TaskID: signing.ID, Kind: "delivery", Title: "Land “Sign commits as your git config says” on main", Context: "Signing failures now say signing was the cause, and the tests use a stand-in signer.\n\nApproving moves main in crew-assistant forward to include it. Nothing already on main is replaced. Landing here means: the next release includes it.", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}, Status: "open", CreatedAt: ago(4 * time.Minute)},
		{ID: "demo-changelog-question", ProjectID: docs.ID, TaskID: changelog.ID, Kind: "question", Title: "Reviewer has a question about “Changelog page”", Context: "Should the changelog list patch releases, or only minor ones with their patches linked?", Recommendation: "Answer it, or let the team decide", Choices: []string{"Use your judgment", "Stop"}, Status: "open", CreatedAt: ago(22 * time.Minute)},
		{ID: "demo-approve-plan", ProjectID: memo.ID, TaskID: plan.ID, Kind: "delivery", Title: "Approve “One-page Q4 plan”", Context: "Cut to three priorities, each with how we will know it worked.\n\nIt stays on the project.", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}, Status: "open", CreatedAt: ago(10 * time.Minute)},
	}
	resolved := ago(3 * time.Hour)
	decisions = append(decisions, core.Decision{ID: "demo-land-assets", ProjectID: crew.ID, Kind: "delivery", Title: "Land “Composer asset drop and paste” on main", Context: "Both checks passed.", Recommendation: "Approve", Choices: []string{"Approve", "Request changes"}, Status: "resolved", Disposition: "choice", Answer: "Approve", CreatedAt: ago(4 * time.Hour), ResolvedAt: &resolved})

	return core.Snapshot{
		Projects:  []core.Project{crew, docs, memo, reading},
		Tasks:     tasks,
		Decisions: decisions,
		Messages: []core.Message{
			{ID: "demo-m1", Role: "user", Content: "Land the signing change once QA passes.", CreatedAt: ago(40 * time.Minute)},
			{ID: "demo-m2", Role: "assistant", Content: "QA is running `make check` on round 2. I've set a wake-up for when it finishes and will put the landing in your inbox.", CreatedAt: ago(39 * time.Minute)},
			{ID: "demo-m3", Role: "user", Origin: "wake", Content: wakeMessage(ago), CreatedAt: ago(5 * time.Minute)},
			{ID: "demo-m4", Role: "assistant", Content: "`make check` passed. It's in your inbox, ready to land on main.", CreatedAt: ago(4 * time.Minute)},
		},
		Members: []core.Member{
			{ID: "demo-ada", Name: "Ada", Kind: core.RoleImplementer, Engine: "claude", Model: "opus", Instructions: "Prefer small, reviewable commits.", CreatedAt: ago(20 * 24 * time.Hour),
				Avatar: config.Avatar{Background: "#1d1b2e", Accent: "#c3b1e1", Marks: []config.Mark{{D: "M64 22 L100 104 H80 L72 84 H56 L48 104 H28 Z", Color: "#c3b1e1"}, {D: "M60 70 H68 L64 58 Z", Color: "#1d1b2e"}}},
				Learnings: []core.Learning{
					{ID: "demo-l1", Text: "Run the whole test suite before finishing, not only the package you changed.", ProjectID: crew.ID, At: ago(9 * 24 * time.Hour)},
					{ID: "demo-l2", Text: "Keep user-facing copy plain; the owner rewrites filler.", ProjectID: crew.ID, At: ago(2 * 24 * time.Hour)},
				}},
			{ID: "demo-rune", Name: "Rune", Kind: core.RoleReviewer, Engine: "codex", Instructions: "Read the tests before the code.", CreatedAt: ago(20 * 24 * time.Hour),
				Avatar: config.Avatar{Background: "#10202b", Accent: "#8ecae6", Marks: []config.Mark{{D: "M40 30 H80 A22 22 0 0 1 80 74 H52 L88 104", Color: "#8ecae6", StrokeWidth: 12}, {D: "M40 30 V104", Color: "#8ecae6", StrokeWidth: 12}}},
				Learnings: []core.Learning{
					{ID: "demo-l3", Text: "Ask for a test of the failure path, not just the happy one.", ProjectID: crew.ID, At: ago(5 * 24 * time.Hour)},
				}},
		},
		Memories: []core.Memory{
			{ID: "demo-mem1", Key: "decisions", Content: "Bring a recommendation with every decision, and keep updates brief.", Kind: "preference", Source: "owner", UpdatedAt: ago(12 * 24 * time.Hour)},
			{ID: "demo-mem2", Key: "crew-landing", Content: "Land crew-assistant changes by fast-forward to main.", Kind: "preference", Source: "owner", UpdatedAt: ago(26 * time.Hour)},
			{ID: "demo-mem3", Key: "docs-approvals", Content: "docs-site pull requests need two approvals.", Kind: "observation", Source: "assistant", UpdatedAt: ago(42 * 24 * time.Hour)},
		},
		Activity: []core.Activity{
			{ID: "demo-a1", ProjectID: crew.ID, Kind: "task.landed", Summary: "Next-message suggestions landed on main", CreatedAt: ago(150 * time.Minute)},
			{ID: "demo-a2", ProjectID: crew.ID, Kind: "task.landed", Summary: "Composer asset drop and paste landed on main", CreatedAt: ago(3 * time.Hour)},
			{ID: "demo-a3", ProjectID: crew.ID, Kind: "decision.opened", Summary: "Land “Sign commits as your git config says” on main", CreatedAt: ago(4 * time.Minute)},
			{ID: "demo-a4", ProjectID: docs.ID, Kind: "decision.opened", Summary: "Reviewer has a question about “Changelog page”", CreatedAt: ago(22 * time.Minute)},
			{ID: "demo-a5", ProjectID: memo.ID, Kind: "decision.opened", Summary: "Approve “One-page Q4 plan”", CreatedAt: ago(10 * time.Minute)},
		},
	}
}

// wakeMessage is a wake-up as the assistant receives one.
func wakeMessage(ago func(time.Duration) time.Time) string {
	stamp := func(d time.Duration) string { return ago(d).Format(time.RFC3339) }
	return "[Wake-up from the daemon, not a message from the owner. You asked to be woken; act on your continuation if it still applies, and tell the owner only what they need to know.]\n\n" +
		"wake-3f9a12c0 — waiting on task demo-signing\n" +
		"  registered " + stamp(39*time.Minute) + " · seen " + stamp(6*time.Minute) + " · delivered " + stamp(5*time.Minute) + " (1m0s after it was seen)\n" +
		"  before: \"reviewing\" · after: \"waiting\"\n" +
		"  what happened: QA passed round 2 of “Sign commits as your git config says”\n" +
		"  your continuation: put the landing in the owner's inbox\n" +
		"Things may have changed since each was seen; check the current state before acting on it."
}

// writeDrafts puts the writing project's drafts where its team keeps them, so
// they can be read in the dashboard.
func writeDrafts(s core.Snapshot) error {
	for _, p := range s.Projects {
		if p.Playbook == nil || p.Playbook.Medium != core.MediumDocuments {
			continue
		}
		docs, err := localdocs.Open(p.ScratchDirectory)
		if err != nil {
			return err
		}
		for _, t := range s.Tasks {
			if t.ProjectID != p.ID {
				continue
			}
			for _, r := range t.Revisions {
				if err := os.WriteFile(filepath.Join(docs.Workspace(), "q4-plan.md"), []byte(planDraft[r.N-1]), 0600); err != nil {
					return err
				}
				if _, err := docs.Snapshot(t.ID, r.N); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var planDraft = []string{
	"# Q4 plan\n\n1. Activation\n2. Onboarding\n3. Reliability\n4. Pricing page\n",
	"# Q4 plan\n\nThree priorities, in order.\n\n## 1. Activation\nMore new teams reach their first finished project in their first week.\n\n**We'll know it worked when** 40% of new teams finish a project within seven days, up from 25%.\n\n## 2. Reliability\nWork that stops overnight picks up again on its own.\n\n**We'll know it worked when** fewer than 1 in 50 tasks needs a manual retry.\n\n## 3. Pricing page\nPeople can tell which plan fits them without asking us.\n\n**We'll know it worked when** pricing questions to support halve.\n",
}
