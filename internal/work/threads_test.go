package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/quota"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
)

// threadRunner plays a writer that leaves a session of its own each turn,
// named for the task and the turn, and reviewers that answer from each
// task's own script, so tasks can run in any order.
type threadRunner struct {
	mu      sync.Mutex
	reviews map[string][]string
	writes  map[string]int
	seen    []roles.Spec
	// onWriter runs before a writer's turn for a task, with its count.
	onWriter func(objective string, n int)
}

// objectiveOf is the task a turn's prompt is for.
func objectiveOf(prompt string) string {
	_, rest, _ := strings.Cut(prompt, "This task: ")
	objective, _, _ := strings.Cut(rest, "\n")
	return objective
}

func (r *threadRunner) Run(_ context.Context, spec roles.Spec) (roles.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, spec)
	task := objectiveOf(spec.Prompt)
	if !spec.Write {
		if len(r.reviews[task]) == 0 {
			return roles.Result{}, errors.New("no scripted review left for " + task)
		}
		reply := r.reviews[task][0]
		r.reviews[task] = r.reviews[task][1:]
		return roles.Result{Text: reply}, nil
	}
	if r.writes == nil {
		r.writes = map[string]int{}
	}
	r.writes[task]++
	n := r.writes[task]
	if r.onWriter != nil {
		r.onWriter(task, n)
	}
	if err := os.WriteFile(filepath.Join(spec.WorkDir, "note.md"), []byte(fmt.Sprintf("%s %d", task, n)), 0600); err != nil {
		return roles.Result{}, err
	}
	return roles.Result{Text: fmt.Sprintf("Wrote %s, draft %d.", task, n), Session: sessionRef(task, n)}, nil
}

func sessionRef(task string, n int) []byte {
	return fmt.Appendf(nil, `{"engine":"claude","id":%q}`, fmt.Sprintf("%s-%d", task, n))
}

// writerTurns are the writer's turns for one task, in order.
func (r *threadRunner) writerTurns(task string) []roles.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []roles.Spec
	for _, spec := range r.seen {
		if spec.Write && objectiveOf(spec.Prompt) == task {
			out = append(out, spec)
		}
	}
	return out
}

// threadsApp is a writing project whose writer is Ada, a team member, with
// the loop driven by runner.
func threadsApp(t *testing.T, runner *threadRunner) (*Loop, core.Project, core.Member) {
	t.Helper()
	a := testLoop(t)
	a.runner = runner
	a.meter = &quota.Meter{Inspect: func(context.Context, harness.Provider) (harness.AccountReport, error) {
		return harness.AccountReport{}, nil
	}}
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Write notes", Criteria: []string{"Warm tone"}}})
	if err != nil {
		t.Fatal(err)
	}
	ada, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID}); err != nil {
		t.Fatal(err)
	}
	return a, p, ada
}

func taskByID(t *testing.T, a *Loop, id string) core.Task {
	t.Helper()
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snap.Tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("no task %s", id)
	return core.Task{}
}

// Two tasks written by the same member each carry on their own
// conversation, and neither's prompts, replies or session reach the other.
func TestTwoTasksOfOneMemberNeverShareAConversation(t *testing.T) {
	t.Parallel()
	runner := &threadRunner{reviews: map[string][]string{
		"Picnic note":  {revise, pass},
		"Harbour note": {revise, pass},
	}}
	a, p, ada := threadsApp(t, runner)
	ctx := context.Background()
	picnic, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Picnic note", Criteria: []string{"Mention the picnic"}})
	if err != nil {
		t.Fatal(err)
	}
	harbour, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Harbour note", Criteria: []string{"Mention the harbour"}})
	if err != nil {
		t.Fatal(err)
	}
	// Each task has its own exchange with the team, already answered.
	said := map[string][2]string{
		picnic.ID:  {"Bring up the lemonade", "Noted, the lemonade goes in"},
		harbour.ID: {"Name the lighthouse", "Noted, the lighthouse gets named"},
	}
	for id, msg := range said {
		if _, err := a.Core.UpdateTask(ctx, id, func(t *core.Task, _ *core.Project) (string, error) {
			t.Messages = append(t.Messages, core.TeamMessage{ID: "m-" + id, To: "Reviewer", Kind: core.RoleReviewer, From: "Owner", Text: msg[0], Status: core.MessageAnswered, Reply: msg[1]})
			return "", nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	settle(t, a)
	for _, c := range []struct {
		id, task, other, secret string
		mine, theirs            [2]string
	}{
		{picnic.ID, "Picnic note", "Harbour note", "picnic", said[picnic.ID], said[harbour.ID]},
		{harbour.ID, "Harbour note", "Picnic note", "harbour", said[harbour.ID], said[picnic.ID]},
	} {
		done := taskByID(t, a, c.id)
		if len(done.Revisions) != 2 {
			t.Fatalf("%s: %d drafts", c.task, len(done.Revisions))
		}
		writes := runner.writerTurns(c.task)
		if len(writes) != 2 || writes[0].Resume != nil || string(writes[1].Resume) != string(sessionRef(c.task, 1)) {
			t.Fatalf("%s should start its own conversation and carry it on: %+v", c.task, writes)
		}
		for _, w := range writes {
			for _, prompt := range []string{w.Prompt, w.FreshPrompt} {
				if strings.Contains(prompt, c.other) || strings.Contains(strings.ToLower(prompt), otherSecret(c.secret)) || strings.Contains(prompt, c.theirs[0]) || strings.Contains(prompt, c.theirs[1]) {
					t.Fatalf("%s was told about the other task: %s", c.task, prompt)
				}
			}
		}
		// Starting afresh, whether on the first round or when the second
		// can't carry on, the writer gets this task's exchange and its
		// replies; a resumed conversation already has them.
		for _, prompt := range []string{writes[0].Prompt, writes[1].FreshPrompt} {
			if !strings.Contains(prompt, "Owner said to Reviewer: "+c.mine[0]) || !strings.Contains(prompt, "Reviewer replied: "+c.mine[1]) {
				t.Fatalf("%s's fresh start lacks its own messages: %s", c.task, prompt)
			}
		}
		if strings.Contains(writes[1].Prompt, c.mine[0]) {
			t.Fatalf("%s's resumed round was told its history again: %s", c.task, writes[1].Prompt)
		}
		if len(done.Threads) != 1 {
			t.Fatalf("%s threads %+v", c.task, done.Threads)
		}
		th := done.Threads[0]
		if th.Kind != core.RoleImplementer || th.Member != ada.ID || th.Seat != "Ada" || th.Engine != "claude" || string(th.Session) != string(sessionRef(c.task, 2)) {
			t.Fatalf("%s thread %+v", c.task, th)
		}
	}
}

// Work landing during a revise round is merged in, but the round still owes
// every finding of the latest draft: a resumed conversation has never seen
// them, so they follow the merge, once, however the round starts.
func TestACatchUpDuringARevisionStillCarriesItsFindings(t *testing.T) {
	t.Parallel()
	p := core.Project{Brief: core.Brief{Goal: "Add features"}, Playbook: &core.Playbook{Medium: core.MediumGit}}
	task := core.Task{Objective: "Add Feature", Base: "abc",
		Revisions: []core.Revision{{N: 1, Summary: "First try"}, {N: 2, Summary: "Second try"}},
		Verdicts: []core.Verdict{
			{Revision: 1, Role: "Reviewer", Outcome: core.VerdictRevise, Summary: "Missing tests.", Findings: []core.Finding{{Note: "Add a test"}}},
			{Revision: 2, Role: "QA", Outcome: core.VerdictRevise, Summary: "The build fails.", Findings: []core.Finding{{Note: "Fix the vet warning"}}},
			{Revision: 2, Role: "Reviewer", Outcome: core.VerdictRevise, Summary: "Two gaps.", Findings: []core.Finding{{Criterion: "Handles errors", Note: "Check the write error"}, {Note: "Name the flag"}}},
		}}
	caughtUp := catchUpText("main moved on", nil)
	fresh := writerPrompt(p, task, caughtUp, true)
	if !strings.Contains(fresh, "- Draft 1: First try\n  - Reviewer, revise: Missing tests.\n    - Add a test") {
		t.Fatalf("a fresh catch-up round lacks the earlier draft's history: %s", fresh)
	}
	for name, prompt := range map[string]string{"fresh": fresh, "resumed": writerPrompt(p, task, caughtUp, false)} {
		merged := strings.Index(prompt, caughtUp)
		owed := strings.Index(prompt, "Merging is not the whole round: also address every finding below.")
		if merged < 0 || owed < merged {
			t.Fatalf("a %s catch-up round should say what landed, then that the findings are still owed: %s", name, prompt)
		}
		for _, want := range []string{"- Fix the vet warning", "- [Handles errors] Check the write error", "- Name the flag"} {
			if strings.Count(prompt, want) != 1 || strings.Index(prompt, want) < owed {
				t.Fatalf("a %s catch-up round should carry %q once, after the merge: %s", name, want, prompt)
			}
		}
	}
	// Outside a catch-up the checks follow the history, so they appear once.
	if plain := writerPrompt(p, task, "", true); strings.Count(plain, "Fix the vet warning") != 1 || strings.Contains(plain, "Merging is not") {
		t.Fatalf("a fresh round should see the latest checks once: %s", plain)
	}
}

// Catching up a draft that passed, to land it, owes only the merge: the
// history carries its checks, and nothing asks for more changes.
func TestACatchUpBeforeLandingOwesOnlyTheMerge(t *testing.T) {
	t.Parallel()
	p := core.Project{Brief: core.Brief{Goal: "Add features"}, Playbook: &core.Playbook{Medium: core.MediumGit}}
	task := core.Task{Objective: "Add Feature", Base: "abc",
		Revisions: []core.Revision{{N: 1, Summary: "First try"}},
		Verdicts:  []core.Verdict{{Revision: 1, Role: "Reviewer", Outcome: core.VerdictPass, Summary: "Good.", Findings: []core.Finding{{Note: "Rename later"}}}},
	}
	caughtUp := catchUpText("main moved on", []string{"main.go"})
	for name, prompt := range map[string]string{"fresh": writerPrompt(p, task, caughtUp, true), "resumed": writerPrompt(p, task, caughtUp, false)} {
		if !strings.Contains(prompt, caughtUp) || strings.Contains(prompt, "Merging is not") || strings.Contains(prompt, "Improve it in place") {
			t.Fatalf("a %s catch-up before landing should ask only for the merge: %s", name, prompt)
		}
	}
	if fresh := writerPrompt(p, task, caughtUp, true); !strings.Contains(fresh, "  - Reviewer, pass: Good.\n    - Rename later") {
		t.Fatalf("a fresh catch-up before landing should carry the checks in its history: %s", fresh)
	}
}

func otherSecret(secret string) string {
	if secret == "picnic" {
		return "harbour"
	}
	return "picnic"
}

// A seat carries on a task's conversation only when it is the member's who
// started it, on the same engine and model. Any other seat starts afresh
// from the task's record, and leaves the first seat's conversation for it
// to carry on when it takes the task back.
func TestOnlyTheSameMemberOnTheSameEngineAndModelCarriesOnAConversation(t *testing.T) {
	t.Parallel()
	const task = "Thank-you note"
	runner := &threadRunner{reviews: map[string][]string{task: {revise, revise, revise, revise, pass}}}
	a, p, taskRecord := loopApp(t, &scriptedRunner{}, "")
	a.runner = runner
	ctx := context.Background()
	playbook := *p.Playbook
	playbook.MaxRounds = 6
	if _, err := a.Core.SetPlaybook(ctx, p.ID, playbook); err != nil {
		t.Fatal(err)
	}
	// Seats as later rounds find them: the template's Writer belongs to no
	// member; Ada is a member's seat.
	var writer core.Role
	seat := func(change func(r *core.Role)) {
		if _, err := a.Core.UpdateTask(ctx, taskRecord.ID, func(t *core.Task, _ *core.Project) (string, error) {
			for i := range t.Roles {
				if t.Roles[i].Holds(core.RoleImplementer) {
					if writer.Name == "" {
						writer = t.Roles[i]
					}
					r := writer
					change(&r)
					t.Roles[i] = r
				}
			}
			return "", nil
		}); err != nil {
			t.Error(err)
		}
	}
	runner.onWriter = func(_ string, n int) {
		switch n {
		case 1: // Round 2 goes to another member.
			seat(func(r *core.Role) { r.Name, r.Member = "Ada", "ada" })
		case 2: // Round 3 comes back to the Writer.
			seat(func(r *core.Role) {})
		case 3: // Round 4: the Writer on another model.
			seat(func(r *core.Role) { r.Model = "other-model" })
		case 4: // Round 5: the Writer on another engine.
			seat(func(r *core.Role) { r.Engine = "codex" })
		}
	}
	done := settle(t, a)
	writes := runner.writerTurns(task)
	if len(writes) != 5 || len(done.Revisions) != 5 {
		t.Fatalf("%d turns, %d drafts", len(writes), len(done.Revisions))
	}
	fresh := func(i int, why string) {
		t.Helper()
		w := writes[i]
		if w.Resume != nil || !strings.Contains(w.Prompt, "You are starting afresh on this task") || !strings.Contains(w.Prompt, fmt.Sprintf("- Draft %d: Wrote %s, draft %d.", i, task, i)) {
			t.Fatalf("round %d (%s) should start afresh from the record: %+v", i+1, why, w)
		}
		if i > 1 && !strings.Contains(w.Prompt, "- Draft 1: Wrote "+task+", draft 1.\n  - Reviewer, revise: Too formal.\n    - Soften the opening") {
			t.Fatalf("round %d (%s) should see every earlier draft and its checks: %s", i+1, why, w.Prompt)
		}
	}
	if writes[0].Resume != nil || strings.Contains(writes[0].Prompt, "starting afresh") {
		t.Fatalf("the first round has no history: %s", writes[0].Prompt)
	}
	fresh(1, "another member")
	// The Writer carries on its own conversation, not Ada's, and is given the
	// record only in case the engine can't resume it.
	if back := writes[2]; string(back.Resume) != string(sessionRef(task, 1)) || strings.Contains(back.Prompt, "starting afresh") || !strings.Contains(back.FreshPrompt, "- Draft 2: Wrote "+task+", draft 2.") {
		t.Fatalf("the Writer should carry on its own conversation: %+v", back)
	}
	fresh(3, "another model")
	fresh(4, "another engine")
	if len(done.Threads) != 2 {
		t.Fatalf("threads %+v", done.Threads)
	}
	mine, ok := done.Thread(core.RoleImplementer, core.Role{Name: "Writer"})
	if !ok || string(mine.Session) != string(sessionRef(task, 5)) || mine.Engine != "codex" || mine.Model != writer.Model {
		t.Fatalf("the Writer's thread %+v", mine)
	}
	adas, ok := done.Thread(core.RoleImplementer, core.Role{Name: "Someone", Member: "ada"})
	if !ok || string(adas.Session) != string(sessionRef(task, 2)) || adas.Seat != "Ada" {
		t.Fatalf("Ada's thread %+v", adas)
	}
}

// errCrash stands in for the daemon stopping dead mid-turn: nothing after it
// is recorded.
var errCrash = errors.New("the daemon stopped")

// crashingSettle runs loop steps until one crashes mid-turn.
func crashingSettle(t *testing.T, a *Loop) {
	t.Helper()
	defer func() {
		if r := recover(); r != errCrash {
			panic(r)
		}
	}()
	settle(t, a)
	t.Fatal("the turn never crashed")
}

// After a restart, each task carries on its own conversation from the last
// round it recorded: a round cut off mid-turn runs once more, neither lost
// nor recorded twice, and never in another task's conversation.
func TestARestartCarriesEachTaskOnInItsOwnConversation(t *testing.T) {
	t.Parallel()
	runner := &threadRunner{reviews: map[string][]string{
		// The reviewer that asked judges the draft again with the answer.
		"Picnic note":  {ask, revise, pass},
		"Harbour note": {revise, pass},
	}}
	a, p, _ := threadsApp(t, runner)
	ctx := context.Background()
	picnic, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Picnic note"})
	harbour, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Harbour note"})
	runner.onWriter = func(task string, n int) {
		if task == "Harbour note" && n == 2 {
			panic(errCrash)
		}
	}
	crashingSettle(t, a)
	// The picnic waits on the owner with its first round recorded; the
	// harbour's second round was cut off.
	waiting := taskByID(t, a, picnic.ID)
	if cut := taskByID(t, a, harbour.ID); waiting.Status != core.TaskWaiting || cut.Status != core.TaskWriting || len(cut.Revisions) != 1 || len(waiting.Threads) != 1 || len(cut.Threads) != 1 {
		t.Fatalf("before the restart: %+v / %+v", waiting, cut)
	}
	if _, err := a.Core.AnswerDecision(ctx, waiting.DecisionID, "The whole team"); err != nil {
		t.Fatal(err)
	}
	runner.onWriter = nil
	restarted := New(a.Core, a.Config, false)
	restarted.runner, restarted.meter = runner, a.meter
	if err := restarted.resume(ctx); err != nil {
		t.Fatal(err)
	}
	settle(t, restarted)
	for _, c := range []struct{ id, task string }{{picnic.ID, "Picnic note"}, {harbour.ID, "Harbour note"}} {
		done := taskByID(t, restarted, c.id)
		if len(done.Revisions) != 2 {
			t.Fatalf("%s: %d drafts after the restart", c.task, len(done.Revisions))
		}
		writes := runner.writerTurns(c.task)
		last := writes[len(writes)-1]
		if string(last.Resume) != string(sessionRef(c.task, 1)) {
			t.Fatalf("%s should carry on its own recorded conversation: %s", c.task, last.Resume)
		}
		if len(done.Threads) != 1 || string(done.Threads[0].Session) != string(sessionRef(c.task, len(writes))) {
			t.Fatalf("%s threads %+v", c.task, done.Threads)
		}
	}
	if n := len(runner.writerTurns("Harbour note")); n != 3 {
		t.Fatalf("the harbour's cut-off round should run once more: %d turns", n)
	}
}
