//go:build !windows

package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/github"
)

// stackPR is one pull request on stackGitHub.
type stackPR struct {
	number          int
	head, base      string
	state, mergedAs string
	// mergedHead is the head it merged with, once its branch is gone.
	mergedHead string
	ready      bool
}

// stackGitHub stands in for GitHub with several pull requests at once, as a
// stack has: a bare repository takes the pushes, and a squash merge lands
// on the pull request's base there and deletes its branch, as the backend
// repository does.
type stackGitHub struct {
	mu     sync.Mutex
	t      *testing.T
	remote string
	prs    []*stackPR
	// bases are the pull requests' bases the loop changed, and merges what
	// it merged.
	bases  [][]string
	merges []int
}

func (f *stackGitHub) pr(number int) *stackPR {
	for _, pr := range f.prs {
		if pr.number == number {
			return pr
		}
	}
	return nil
}

func (f *stackGitHub) head(branch string) string {
	return ownerGit(f.t, f.remote, "rev-parse", "refs/heads/"+branch)
}

func (f *stackGitHub) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	number := func() *stackPR {
		if n, err := strconv.Atoi(args[2]); err == nil {
			return f.pr(n)
		}
		return nil
	}
	switch strings.Join(args[:2], " ") {
	case "api graphql":
		if !strings.Contains(args[3], "reviewThreads") {
			return []byte("{}"), nil
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(args[len(args)-1], "number="))
		pr := f.pr(n)
		var commit any
		if pr.mergedAs != "" {
			commit = map[string]string{"oid": pr.mergedAs}
		}
		return json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{"state": pr.state, "headRefOid": f.headOf(pr), "mergeCommit": commit, "mergeQueueEntry": nil, "reviewThreads": map[string]any{"nodes": []any{}}}}}})
	case "pr list":
		head := argAfter(args, "--head")
		for _, pr := range f.prs {
			if pr.head == head && pr.state == "OPEN" {
				return []byte(fmt.Sprintf(`[{"number": %d, "url": "https://github.com/o/r/pull/%d"}]`, pr.number, pr.number)), nil
			}
		}
		return []byte("[]"), nil
	case "pr create":
		pr := &stackPR{number: 7 + len(f.prs), head: argAfter(args, "--head"), base: argAfter(args, "--base"), state: "OPEN"}
		f.prs = append(f.prs, pr)
		return []byte(fmt.Sprintf("https://github.com/o/r/pull/%d\n", pr.number)), nil
	case "pr edit":
		if base := argAfter(args, "--base"); base != "" {
			f.bases = append(f.bases, args)
			number().base = base
		}
		return nil, nil
	case "pr comment", "pr ready":
		return nil, nil
	case "pr merge":
		pr := number()
		sha := argAfter(args, "--match-head-commit")
		if pr.state != "OPEN" || sha != f.head(pr.head) {
			return nil, errors.New("head moved")
		}
		base := f.head(pr.base)
		tree := strings.Fields(ownerGit(f.t, f.remote, "merge-tree", "--write-tree", base, sha))[0]
		pr.mergedAs = ownerGit(f.t, f.remote, "commit-tree", tree, "-p", base, "-m", fmt.Sprintf("Squashed #%d", pr.number))
		ownerGit(f.t, f.remote, "update-ref", "refs/heads/"+pr.base, pr.mergedAs)
		ownerGit(f.t, f.remote, "update-ref", "-d", "refs/heads/"+pr.head)
		pr.state, pr.mergedHead = "MERGED", sha
		f.merges = append(f.merges, pr.number)
		return nil, nil
	case "pr view":
		pr := number()
		view := map[string]any{"number": pr.number, "url": fmt.Sprintf("https://github.com/o/r/pull/%d", pr.number), "state": pr.state, "isDraft": false, "mergeable": "MERGEABLE",
			"mergeStateStatus": "BLOCKED", "reviewDecision": "REVIEW_REQUIRED", "headRefOid": f.headOf(pr), "baseRefName": pr.base, "reviews": []any{}, "comments": []any{},
			"statusCheckRollup": []map[string]string{{"name": "build", "status": "IN_PROGRESS"}}}
		if pr.ready {
			view["mergeStateStatus"], view["reviewDecision"] = "CLEAN", "APPROVED"
			view["statusCheckRollup"] = []map[string]string{{"name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"}}
		}
		if pr.mergedAs != "" {
			view["mergeCommit"] = map[string]string{"oid": pr.mergedAs}
		}
		return json.Marshal(view)
	}
	return nil, fmt.Errorf("unexpected gh %v", args)
}

// headOf is the head a pull request had, which a merged one's deleted
// branch no longer says.
func (f *stackGitHub) headOf(pr *stackPR) string {
	if pr.state == "MERGED" {
		return pr.mergedHead
	}
	return f.head(pr.head)
}

func (f *stackGitHub) set(fn func()) { f.mu.Lock(); defer f.mu.Unlock(); fn() }

// fakeG2G records each run of g2g, and fails the runs fail says to.
type fakeG2G struct {
	mu    sync.Mutex
	calls []g2gCall
	fail  func(args []string) bool
	found string
}

func (g *fakeG2G) tool(version string) g2gTool {
	return g2gTool{
		find: func(context.Context) (string, string) {
			if version == "" {
				return "", ""
			}
			return "/opt/fake/bin/g2g", version
		},
		run: func(_ context.Context, c g2gCall) ([]byte, error) {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls = append(g.calls, c)
			if g.fail != nil && g.fail(c.Args) {
				return nil, errors.New("g2g github comment: gh: HTTP 502")
			}
			if g.found != "" {
				return []byte(g.found), errG2GFound
			}
			return []byte(`{"schemaVersion": 3}`), nil
		},
	}
}

func (g *fakeG2G) ran(args ...string) (g2gCall, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, c := range g.calls {
		if slices.Equal(c.Args, args) {
			return c, true
		}
	}
	return g2gCall{}, false
}

// stackScenario is a code project landing by pull request into main with
// stacking on, and two tasks: A, and B stacked on it.
type stackScenario struct {
	a      *Loop
	ctx    context.Context
	gh     *stackGitHub
	g2g    *fakeG2G
	remote string
	p      core.Project
	parent string
	child  string
	// clock is when the loop's wakes are next polled, a little later each
	// time, as time passes.
	clock time.Time
}

// poll lets the loop's wakes notice what changed on GitHub, and settles it.
func (s *stackScenario) poll(t *testing.T) {
	t.Helper()
	s.clock = s.clock.Add(2 * time.Minute)
	if err := s.a.checkWakes(s.ctx, s.clock); err != nil {
		t.Fatal(err)
	}
	settle(t, s.a)
}

func newStackScenario(t *testing.T) *stackScenario {
	t.Helper()
	source := ownerRepo(t)
	remote := t.TempDir()
	ownerGit(t, remote, "init", "-q", "--bare")
	ownerGit(t, source, "push", "-q", remote, "main")
	passes := make([]string, 40)
	for i := range passes {
		passes[i] = pass
	}
	// Each implementer's turn adds a file of its own, so what each task
	// changed can be told apart wherever it ends up.
	runner := &codeRunner{scriptedRunner: scriptedRunner{reviews: passes}, onEdit: func(dir string, n int) bool {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("part%d.go", n)), []byte("package main\n"), 0o600)
		return false
	}}
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	gh := &stackGitHub{t: t, remote: remote}
	a.github = github.Client{Run: gh.run}
	a.githubURL = func(string) string { return remote }
	g2g := &fakeG2G{}
	a.stacker = g2g.tool("v0.41.0")
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetLanding(ctx, p.ID, core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Merge: "squash", Open: core.OpenImplementer, Approve: core.ApproveNone, Stack: true}); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	a.StopTask(ctx, snap.Tasks[0].ProjectID, snap.Tasks[0].ID)
	parent, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add A"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add B", StacksOn: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	return &stackScenario{a: a, ctx: ctx, gh: gh, g2g: g2g, remote: remote, p: p, parent: parent.ID, child: child.ID, clock: time.Now()}
}

func (s *stackScenario) task(t *testing.T, id string) core.Task {
	t.Helper()
	snap, _ := s.a.Core.Snapshot(s.ctx)
	task, ok := findTask(snap, s.p.ID, id)
	if !ok {
		t.Fatal("the task is gone")
	}
	return task
}

// ready has GitHub call pull request n approved and green, and lets the
// loop's wakes notice.
func (s *stackScenario) ready(t *testing.T, n int) {
	t.Helper()
	s.gh.set(func() { s.gh.pr(n).ready = true })
	s.poll(t)
}

// opened settles the loop until both tasks wait on their pull requests:
// A's onto main, B's onto A's branch, B built on what A pushed.
func (s *stackScenario) opened(t *testing.T) (parent, child core.Task) {
	t.Helper()
	settle(t, s.a)
	parent, child = s.task(t, s.parent), s.task(t, s.child)
	if parent.Status != core.TaskAwaiting || !parent.PROpen() || child.Status != core.TaskAwaiting || !child.PROpen() {
		t.Fatalf("not both open: %s %s / %s %s", parent.Status, parent.Detail, child.Status, child.Detail)
	}
	if child.Base != parent.Proposal.Pushed || child.From != parent.Proposal.Branch || !child.OnParent() {
		t.Fatalf("B didn't start from A's pull request: base %s from %s, A pushed %s on %s", child.Base, child.From, parent.Proposal.Pushed, parent.Proposal.Branch)
	}
	if pr := s.gh.pr(child.Proposal.Number); pr.base != parent.Proposal.Branch || child.Proposal.Base != parent.Proposal.Branch {
		t.Fatalf("B's pull request merges into %s (recorded %s), not A's branch %s", pr.base, child.Proposal.Base, parent.Proposal.Branch)
	}
	if s.gh.pr(parent.Proposal.Number).base != "main" {
		t.Fatal("A's pull request doesn't merge into main")
	}
	return parent, child
}

// files lists what a commit on the stand-in for GitHub holds.
func (s *stackScenario) files(t *testing.T, commit string) []string {
	return strings.Fields(ownerGit(t, s.remote, "ls-tree", "--name-only", commit))
}

// A task stacked on another starts from its open pull request, opens its own
// onto it, and is held from merging until that has merged; then it is
// replayed onto the target, its pull request pointed there, and it merges.
func TestAStackedTaskMergesAfterTheOneBelowIt(t *testing.T) {
	t.Parallel()
	s := newStackScenario(t)
	parent, child := s.opened(t)

	s.ready(t, child.Proposal.Number)
	child = s.task(t, s.child)
	if child.Status != core.TaskAwaiting || !strings.Contains(child.Detail, "it is stacked on “Add A”, whose pull request has not merged") || len(s.gh.merges) != 0 {
		t.Fatalf("B wasn't held for A: %s %s, merges %v", child.Status, child.Detail, s.gh.merges)
	}

	s.ready(t, parent.Proposal.Number)
	parent, child = s.task(t, s.parent), s.task(t, s.child)
	if parent.Status != core.TaskLanded || child.Status != core.TaskLanded || !slices.Equal(s.gh.merges, []int{parent.Proposal.Number, child.Proposal.Number}) {
		t.Fatalf("A %s, B %s %s, merges %v", parent.Status, child.Status, child.Detail, s.gh.merges)
	}
	replayed := slices.ContainsFunc(child.Revisions, func(r core.Revision) bool { return strings.HasPrefix(r.Summary, "Replayed onto main") })
	if !replayed || child.From != "main" {
		t.Fatalf("B was not replayed onto main: from %s, revisions %+v", child.From, child.Revisions)
	}
	want := []string{"pr", "edit", strconv.Itoa(child.Proposal.Number), "--repo", "o/r", "--base", "main"}
	if len(s.gh.bases) != 1 || !slices.Equal(s.gh.bases[0], want) || child.Proposal.Base != "main" {
		t.Fatalf("B's pull request was pointed %v (recorded %s)", s.gh.bases, child.Proposal.Base)
	}
	// main has each change once: A's squash, then B's own.
	main := ownerGit(t, s.remote, "rev-parse", "refs/heads/main")
	if got := s.files(t, main); !slices.Contains(got, "part1.go") || !slices.Contains(got, "part2.go") {
		t.Fatalf("main holds %v", got)
	}
	if n := ownerGit(t, s.remote, "rev-list", "--count", "main"); n != "3" {
		t.Fatalf("main has %s commits, not the start and one squash each", n)
	}
}

// g2g mirrors the stack in the project's clone once it is one, with the
// GitHub repository named for gh; when it fails, delivery goes on and the
// task says so.
func TestG2GMirrorsTheStackWithoutHoldingItUp(t *testing.T) {
	t.Parallel()
	s := newStackScenario(t)
	s.g2g.fail = func(args []string) bool { return slices.Contains(args, "comment") }
	parent, child := s.opened(t)
	track, ok := s.g2g.ran("track", "--branch", child.Proposal.Branch, "--parent", parent.Proposal.Branch, "--apply", "--json")
	if !ok {
		t.Fatalf("B's branch wasn't recorded on A's: %+v", s.g2g.calls)
	}
	if _, ok := s.g2g.ran("track", "--branch", parent.Proposal.Branch, "--parent", "main", "--apply", "--json"); !ok {
		t.Fatalf("A's branch wasn't recorded on main: %+v", s.g2g.calls)
	}
	if _, ok := s.g2g.ran("github", "comment", "--branch", parent.Proposal.Branch, "--apply", "--json"); !ok {
		t.Fatalf("the stack's comments weren't kept: %+v", s.g2g.calls)
	}
	if track.Path != "/opt/fake/bin/g2g" || filepath.Base(track.Dir) != "clone" || !slices.Contains(track.Env, "GH_REPO=o/r") || !slices.Contains(track.Env, "PATH=/opt/fake/bin"+string(os.PathListSeparator)+os.Getenv("PATH")) {
		t.Fatalf("ran as %+v", track)
	}
	// The branches g2g reads are the heads crew pushed.
	for _, task := range []core.Task{parent, child} {
		if at := ownerGit(t, track.Dir, "rev-parse", "refs/heads/"+task.Proposal.Branch); at != task.Proposal.Pushed {
			t.Fatalf("%s is at %s in the clone, pushed %s", task.Proposal.Branch, at, task.Proposal.Pushed)
		}
	}
	if !slices.ContainsFunc(child.Notes, func(n core.Note) bool {
		return n.By == "crew-assistant" && strings.Contains(n.Text, "g2g couldn't record this stack") && strings.Contains(n.Text, "HTTP 502")
	}) {
		t.Fatalf("the failure isn't on the task: %+v", child.Notes)
	}
	for _, c := range s.g2g.calls {
		if slices.ContainsFunc(c.Args, func(a string) bool { return a == "push" || a == "submit" || a == "land" || a == "restack" }) {
			t.Fatalf("g2g was asked to publish: %v", c.Args)
		}
	}
}

// A task stacked on a pull request that closes without merging asks the
// owner, who can have it rebased onto the target with only its own change.
func TestAStackedTaskWhoseParentClosedIsTheOwnersToRebase(t *testing.T) {
	t.Parallel()
	s := newStackScenario(t)
	parent, child := s.opened(t)
	s.gh.set(func() { s.gh.pr(parent.Proposal.Number).state = "CLOSED" })
	s.poll(t)
	child = s.task(t, s.child)
	d := openDecision(t, s.a, child)
	if d.Kind != core.DecisionUnstack || !slices.Equal(d.Choices, []string{core.ChoiceUnstack, choiceStop}) || !strings.Contains(d.Context, "closed without merging") {
		t.Fatalf("not asked: %+v", d)
	}
	if _, err := s.a.Core.ChooseDecision(s.ctx, d.ID, core.ChoiceUnstack, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	settle(t, s.a)
	child = s.task(t, s.child)
	if child.StacksOn != "" || child.From != "main" || child.Status != core.TaskAwaiting || child.Proposal.Base != "main" {
		t.Fatalf("not rebased: stacks on %q from %s, %s %s, base %s", child.StacksOn, child.From, child.Status, child.Detail, child.Proposal.Base)
	}
	if got := s.files(t, child.Proposal.Pushed); slices.Contains(got, "part1.go") || !slices.Contains(got, "part2.go") {
		t.Fatalf("B's pushed head holds %v, not only its own change on main", got)
	}
	if pr := s.gh.pr(child.Proposal.Number); pr.base != "main" {
		t.Fatalf("B's pull request still merges into %s", pr.base)
	}
}

// Once GitHub itself has pointed the pull request at the target, as it does
// when the branch below is deleted, only the record follows.
func TestARetargetGitHubAlreadyMadeIsOnlyRecorded(t *testing.T) {
	t.Parallel()
	a, p, task := loopApp(t, &scriptedRunner{}, "")
	var calls [][]string
	a.github = github.Client{Run: func(_ context.Context, args ...string) ([]byte, error) { calls = append(calls, args); return nil, nil }}
	land := core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r"}
	task.From = "main"
	prop := core.Proposal{Number: 8, Base: "paul/add-a"}
	if done, err := a.retarget(context.Background(), task, land, core.Revision{}, &prop, github.PR{State: "OPEN", BaseRefName: "main"}); done || err != nil || prop.Base != "main" || len(calls) != 0 {
		t.Fatalf("done %v err %v base %s calls %v", done, err, prop.Base, calls)
	}
	// One never stacked is never retargeted, whatever its base says.
	plain := core.Proposal{Number: 9}
	if done, err := a.retarget(context.Background(), task, land, core.Revision{}, &plain, github.PR{State: "OPEN", BaseRefName: "release"}); done || err != nil || len(calls) != 0 {
		t.Fatalf("retargeted a pull request never stacked: %v %v", calls, err)
	}
	_ = p
}

// Stacking is turned on only with a g2g it works with, and stays on.
func TestStackingNeedsG2G(t *testing.T) {
	t.Parallel()
	a, _, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{ownerRepo(t)}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", BranchPrefix: "paul/", Check: "make check"}); err != nil {
		t.Fatal(err)
	}
	land := core.LandPolicy{PullRequests: true, Target: "main", GitHub: "o/r", Stack: true}
	g2g := &fakeG2G{}
	for version, want := range map[string]string{"": "install g2g from Settings → Tools", "v0.40.2": "stacking needs g2g 0.41.0 or later, and 0.40.2 is installed; update g2g from Settings → Tools"} {
		a.stacker = g2g.tool(version)
		if _, err := a.SetLanding(ctx, p.ID, land); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("g2g %q: %v", version, err)
		}
	}
	if _, err := a.SetLanding(ctx, p.ID, core.LandPolicy{Via: core.LandPush, Target: "main", Stack: true}); err == nil || !strings.Contains(err.Error(), "only for changes that land by pull request") {
		t.Fatalf("stacking a push: %v", err)
	}
	a.stacker = g2g.tool("v0.41.0")
	if got, err := a.SetLanding(ctx, p.ID, land); err != nil || !got.Playbook.Land.Stack {
		t.Fatalf("%+v %v", got.Playbook.Land, err)
	}
	// Once on, other landing changes don't ask for g2g again.
	a.stacker = g2g.tool("")
	land.Draft = true
	if got, err := a.SetLanding(ctx, p.ID, land); err != nil || !got.Playbook.Land.Stack || !got.Playbook.Land.Draft {
		t.Fatalf("%+v %v", got.Playbook.Land, err)
	}
}

func callG2G(t *testing.T, tools roleTools, args ...string) session.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"args": args})
	result, err := tools.Handler().CallTool(context.Background(), session.ToolCall{Name: "g2g", Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// The PM stacks tasks where stacking is on, and the PM and implementer read
// the project's stacks with g2g; nothing else is run, and no one else reads.
func TestThePMStacksAndTheTeamReadsStacks(t *testing.T) {
	t.Parallel()
	g2g := &fakeG2G{found: `{"schemaVersion": 3, "findings": [{"branch": "paul/add-b"}]}`}
	stacking := func(_ *testing.T, a *Loop, _ *TeamChoice, land *core.LandPolicy) {
		a.stacker = g2g.tool("v0.41.0")
		land.Stack = true
	}
	s := newPRScenario(t, 2, withPM, stacking)
	ctx := context.Background()
	first := s.current(t)
	second, _ := s.a.Core.QueueTaskAs(ctx, s.p.ID, core.TaskInput{Objective: "Add B"}, core.LinkedByPM)
	third, _ := s.a.Core.QueueTaskAs(ctx, s.p.ID, core.TaskInput{Objective: "Add C"}, core.LinkedByPM)

	pm := s.a.managerTools(s.p.ID, core.Role{Name: "Pim"})
	if !slices.Contains(toolNames(pm), "g2g") || !strings.Contains(pm.guide(), "stack it on that task rather than have it wait") {
		t.Fatalf("the PM isn't told of stacking: %v", toolNames(pm))
	}
	if got := callTool(t, pm, "link_tasks", map[string]string{"task_id": second.ID, "relation": "stacks_on", "other_task_id": first.ID}); got.IsError {
		t.Fatalf("stack: %s", got.Content)
	}
	if got := callTool(t, pm, "link_tasks", map[string]string{"task_id": second.ID, "relation": "stacks_on", "other_task_id": third.ID}); !got.IsError || !strings.Contains(got.Content, "already linked") && !strings.Contains(got.Content, "already stacked") {
		t.Fatalf("a second parent: %+v", got)
	}
	if got := callTool(t, pm, "queue_task", map[string]string{"title": "Add D", "requirements": "", "depends_on": "", "stacks_on": second.Ref}); got.IsError {
		t.Fatalf("queue stacked: %s", got.Content)
	}
	snap, _ := s.a.Core.Snapshot(ctx)
	stacked := slices.IndexFunc(snap.Tasks, func(t core.Task) bool { return t.Objective == "Add D" })
	if stacked < 0 || snap.Tasks[stacked].StacksOn != second.ID {
		t.Fatal("the queued task isn't stacked")
	}

	got := callG2G(t, pm, "doctor", "--json")
	var read g2gResult
	if got.IsError || json.Unmarshal([]byte(got.Content), &read) != nil || !read.Found || !strings.Contains(read.Output, "paul/add-b") {
		t.Fatalf("doctor: %+v", got)
	}
	call, ok := g2g.ran("doctor", "--json")
	if !ok || filepath.Base(call.Dir) != "clone" || !slices.Contains(call.Env, "GH_REPO=o/r") {
		t.Fatalf("ran %+v", g2g.calls)
	}
	for _, args := range [][]string{{"track", "--branch", "x", "--apply"}, {"status"}, {"status", "--json", "--apply"}, {"push", "--apply"}} {
		if got := callG2G(t, pm, args...); !got.IsError {
			t.Fatalf("ran g2g %v", args)
		}
	}
	if len(g2g.calls) != 1 {
		t.Fatalf("ran more than the read: %+v", g2g.calls)
	}

	implementer := s.a.toolsFor(third, core.RoleImplementer, core.Role{})
	if !slices.Contains(toolNames(implementer), "g2g") || callG2G(t, implementer, "status", "--json").IsError {
		t.Fatal("the implementer can't read the stacks")
	}
	if got := callTool(t, implementer, "link_tasks", map[string]string{"relation": "stacks_on", "other_task_id": first.ID}); !got.IsError {
		t.Fatal("the implementer stacked its own task")
	}
	if reviewer := s.a.toolsFor(third, core.RoleReviewer, core.Role{}); slices.Contains(toolNames(reviewer), "g2g") {
		t.Fatal("a reviewer reads the stacks")
	}

	// Without stacking there is neither.
	snap, _ = s.a.Core.Snapshot(ctx)
	p, _ := findProject(snap, s.p.ID)
	land := p.Playbook.Land
	land.Stack = false
	if _, err := s.a.SetLanding(ctx, s.p.ID, land); err != nil {
		t.Fatal(err)
	}
	pm = s.a.managerTools(s.p.ID, core.Role{Name: "Pim"})
	if slices.Contains(toolNames(pm), "g2g") || strings.Contains(pm.guide(), "stacks_on") {
		t.Fatal("the PM is offered stacking where it's off")
	}
	if got := callTool(t, pm, "link_tasks", map[string]string{"task_id": third.ID, "relation": "stacks_on", "other_task_id": first.ID}); !got.IsError || !strings.Contains(got.Content, "stacking is off") {
		t.Fatalf("stacked where it's off: %+v", got)
	}
}
