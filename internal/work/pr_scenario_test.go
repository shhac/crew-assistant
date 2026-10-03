//go:build !windows

package work

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

// fakeGitHub stands in for GitHub: a bare repository takes the pushes, and
// gh's answers come from what the test says the checks and reviews are.
type fakeGitHub struct {
	mu     sync.Mutex
	t      *testing.T
	remote string
	head   string
	opened int
	checks string
	// checksOn is the commit the checks ran on; a newer head is pending, as
	// on GitHub.
	checksOn string
	decision string
	reviews  []github.Review
	comments []github.Comment
	merged   string
	merges   [][]string
	closed   bool
	// threads are the review threads gh's GraphQL answers with; posts are
	// the comments, replies, resolves and edits the loop made.
	threads []map[string]any
	posts   [][]string
	// created is how the pull request was opened.
	created []string
}

func (f *fakeGitHub) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch strings.Join(args[:2], " ") {
	case "api graphql":
		if strings.Contains(args[3], "reviewThreads") {
			return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"reviewThreads": map[string]any{"nodes": f.threads}}}}})
		}
		f.posts = append(f.posts, args)
		return []byte("{}"), nil
	case "pr comment", "pr edit":
		f.posts = append(f.posts, args)
		return nil, nil
	case "pr close":
		f.posts = append(f.posts, args)
		f.closed = true
		return nil, nil
	case "pr list":
		if f.opened == 0 || f.closed {
			return []byte("[]"), nil
		}
		return []byte(`[{"number": 7, "url": "https://github.com/o/r/pull/7"}]`), nil
	case "pr create":
		if f.opened > 0 && !f.closed {
			return nil, fmt.Errorf("a pull request for branch %q already exists", f.head)
		}
		f.opened++
		f.closed = false
		f.created = args
		f.head = args[slices.Index(args, "--head")+1]
		return []byte("https://github.com/o/r/pull/7\n"), nil
	case "pr merge":
		f.merges = append(f.merges, args)
		sha := args[slices.Index(args, "--match-head-commit")+1]
		if sha != ownerGit(f.t, f.remote, "rev-parse", "refs/heads/"+f.head) {
			return nil, fmt.Errorf("head moved")
		}
		f.merged = sha
		return nil, nil
	case "pr view":
		head := ownerGit(f.t, f.remote, "rev-parse", "refs/heads/"+f.head)
		pr := map[string]any{"number": 7, "url": "https://github.com/o/r/pull/7", "state": "OPEN", "mergeable": "MERGEABLE", "mergeStateStatus": "BLOCKED", "reviewDecision": f.decision,
			"headRefOid": head, "reviews": f.reviews, "comments": f.comments}
		checks := f.checks
		if f.checksOn != "" && f.checksOn != head {
			checks = "PENDING"
		}
		switch checks {
		case "FAILURE":
			pr["statusCheckRollup"] = []map[string]string{{"name": "build", "status": "COMPLETED", "conclusion": "FAILURE", "detailsUrl": "https://ci/1"}}
		case "SUCCESS":
			pr["statusCheckRollup"] = []map[string]string{{"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"}}
			if f.decision == "APPROVED" {
				pr["mergeStateStatus"] = "CLEAN"
			}
		default:
			pr["statusCheckRollup"] = []map[string]string{{"name": "build", "status": "IN_PROGRESS"}}
		}
		if f.merged != "" {
			pr["state"], pr["mergeCommit"] = "MERGED", map[string]string{"oid": f.merged}
		}
		if f.closed {
			pr["state"] = "CLOSED"
		}
		return json.Marshal(pr)
	}
	return nil, fmt.Errorf("unexpected gh %v", args)
}

func (f *fakeGitHub) set(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

// postsOf are the posts the loop made of one kind: thread replies, resolves
// or conversation comments.
func (f *fakeGitHub) postsOf(kind string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, args := range f.posts {
		switch {
		case kind == "reply" && args[0] == "api" && strings.Contains(args[3], "addPullRequestReviewThreadReply"),
			kind == "resolve" && args[0] == "api" && strings.Contains(args[3], "resolveReviewThread"),
			kind == "comment" && args[0] == "pr" && args[1] == "comment":
			out = append(out, args)
		}
	}
	return out
}

// prScenario is a code project landing by pull request on a stand-in for
// GitHub, with one task approved and waiting on its open pull request.
type prScenario struct {
	a      *Loop
	ctx    context.Context
	gh     *fakeGitHub
	runner *codeRunner
	remote string
	p      core.Project
	id     string
}

// prSetup changes the team or landing policy a scenario starts with.
type prSetup func(t *testing.T, a *Loop, team *TeamChoice, land *core.LandPolicy)

// withPM seats Pim as the team's PM.
func withPM(t *testing.T, a *Loop, team *TeamChoice, _ *core.LandPolicy) {
	pim, err := a.Core.SaveMember(context.Background(), "", core.MemberInput{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	team.PM = pim.ID
}

func newPRScenario(t *testing.T, reviews int, setups ...prSetup) *prScenario {
	t.Helper()
	source := ownerRepo(t)
	remote := t.TempDir()
	ownerGit(t, remote, "init", "-q", "--bare")
	ownerGit(t, source, "push", "-q", remote, "main")
	passes := make([]string, reviews)
	for i := range passes {
		passes[i] = pass
	}
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	gh := &fakeGitHub{t: t, remote: remote, checks: "PENDING", decision: "REVIEW_REQUIRED"}
	a.github = github.Client{Run: gh.run}
	a.githubURL = func(string) string { return remote }
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add a feature"}})
	if err != nil {
		t.Fatal(err)
	}
	team := TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}
	land := core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Merge: "squash", Open: core.OpenOwner, Approve: core.ApproveNone}
	for _, setup := range setups {
		setup(t, a, &team, &land)
	}
	if _, err = a.SetTeam(ctx, p.ID, team); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetLanding(ctx, p.ID, land); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	return &prScenario{a: a, ctx: ctx, gh: gh, runner: runner, remote: remote, p: p, id: task.ID}
}

// current settles the loop and returns the task.
func (s *prScenario) current(t *testing.T) core.Task {
	t.Helper()
	settle(t, s.a)
	snap, _ := s.a.Core.Snapshot(s.ctx)
	for _, candidate := range snap.Tasks {
		if candidate.ID == s.id {
			return candidate
		}
	}
	t.Fatal("the task is gone")
	return core.Task{}
}

// open approves the first draft and returns the task waiting on its PR.
func (s *prScenario) open(t *testing.T) core.Task {
	t.Helper()
	task := s.current(t)
	s.a.Core.ChooseDecision(s.ctx, openDecision(t, s.a, task).ID, choiceApprove, core.FromOwner)
	task = s.current(t)
	if task.Status != core.TaskAwaiting || task.Proposal == nil || task.Proposal.Number != 7 {
		t.Fatalf("the pull request did not open: %+v", task)
	}
	return task
}

// review has someone ask for changes, and lets the loop's wakes notice.
func (s *prScenario) review(t *testing.T, body string, at time.Time) {
	t.Helper()
	s.gh.set(func() {
		s.gh.reviews = append(s.gh.reviews, github.Review{Author: github.Author{Login: "alice"}, Association: "COLLABORATOR", State: "CHANGES_REQUESTED", Body: body, SubmittedAt: at})
	})
	if err := s.a.checkWakes(s.ctx, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func (s *prScenario) remoteHead(t *testing.T) string {
	return ownerGit(t, s.remote, "rev-parse", "refs/heads/paul/add-a")
}

// readyPR has the scenario's pull request approved and green on its head,
// and lets the loop's wakes notice.
func (s *prScenario) readyPR(t *testing.T) {
	t.Helper()
	s.gh.set(func() { s.gh.checks, s.gh.checksOn, s.gh.decision = "SUCCESS", "", "APPROVED" })
	if err := s.a.checkWakes(s.ctx, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// opensUnasked and mergeBy set who opens and who approves merging.
func opensUnasked(_ *testing.T, _ *Loop, _ *TeamChoice, land *core.LandPolicy) {
	land.Open = core.OpenImplementer
}

func mergeBy(gate string) prSetup {
	return func(_ *testing.T, _ *Loop, _ *TeamChoice, land *core.LandPolicy) { land.Approve = gate }
}

// prBlock is an implementer's ending giving its pull request's text.
func prBlock(title, body string) string {
	return fmt.Sprintf("\n```pr\n{\"title\": %q, \"body\": %q}\n```", title, body)
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// thread is a collaborator's review thread on feature.go.
func thread(id, body string, at time.Time) map[string]any {
	return map[string]any{"id": id, "isResolved": false, "path": "feature.go", "line": 3, "comments": map[string]any{"nodes": []map[string]any{{"author": map[string]string{"login": "alice"}, "authorAssociation": "COLLABORATOR", "body": body, "createdAt": at.Format(time.RFC3339)}}}}
}
