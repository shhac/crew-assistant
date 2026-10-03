package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/testbridge"
)

// browserTeam is a writing team whose writer is a member allowing the
// browser, and a template reviewer that isn't.
func browserTeam(t *testing.T, runner *scriptedRunner) (*Loop, core.Member) {
	return browserTeamOnEngine(t, runner, "claude")
}

func browserTeamOnEngine(t *testing.T, runner *scriptedRunner, engine string) (*Loop, core.Member) {
	t.Helper()
	a, p, _ := loopApp(t, runner, "")
	ctx := context.Background()
	ada, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: engine, Browser: core.Browser{On: true, Name: "Work"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetTeam(ctx, p.ID, TeamChoice{Template: "draft", Implementer: ada.ID}); err != nil {
		t.Fatal(err)
	}
	return a, ada
}

func writerTurnsOf(runner *scriptedRunner) []int {
	var out []int
	for i, spec := range runner.seen {
		if spec.Write {
			out = append(out, i)
		}
	}
	return out
}

// A member who allows the browser has it in every turn they take, whatever
// the role, told what it is for; a seat no member fills has none.
func TestAMembersBrowserPolicyReachesTheirTurns(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, _ := browserTeam(t, runner)
	settle(t, a)
	writers := writerTurnsOf(runner)
	if len(writers) == 0 {
		t.Fatal("no writer turn")
	}
	writer := runner.seen[writers[0]]
	if !writer.Browser || !strings.Contains(writer.Instructions, "only for controlling a browser") || !strings.Contains(writer.Instructions, `named "Work"`) {
		t.Fatalf("the writer's turn: browser %v, instructions %q", writer.Browser, writer.Instructions)
	}
	for _, spec := range runner.seen {
		if !spec.Write && spec.Browser {
			t.Fatalf("the template reviewer was given the browser: %+v", spec)
		}
	}
}

// The policy is read as each turn starts, so withdrawing it takes the
// browser from the member's next turn without re-seating anyone.
func TestWithdrawingTheBrowserReachesTheNextTurn(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}}
	a, ada := browserTeam(t, runner)
	seat := core.Role{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Member: ada.ID}
	if !a.baseSpec(seat, t.TempDir(), "go").Browser {
		t.Fatal("allowed, the turn had no browser")
	}
	if _, err := a.Core.SaveMember(context.Background(), ada.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
	if spec := a.baseSpec(seat, t.TempDir(), "go"); spec.Browser || strings.Contains(spec.Instructions, "browser") {
		t.Fatalf("withdrawn, the turn still had the browser: %+v", spec)
	}
}

// When Chrome isn't connected, a turn given the browser goes on without
// it, told so, rather than failing the task.
func TestATurnWhoseBrowserIsUnreachableGoesOnWithoutIt(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, fail: []error{&session.CapabilityError{Engine: harness.Claude, Code: session.CapabilityBrowserToolsMissing, Phase: session.BeforeFirstPrompt}}}
	a, _ := browserTeam(t, runner)
	task := settle(t, a)
	writers := writerTurnsOf(runner)
	if len(writers) < 2 {
		t.Fatalf("the writer wasn't run again: %d turns", len(writers))
	}
	first, retried := runner.seen[writers[0]], runner.seen[writers[1]]
	if !first.Browser || retried.Browser || !strings.Contains(retried.Instructions, noBrowserNote) {
		t.Fatalf("first %v, retried %v %q", first.Browser, retried.Browser, retried.Instructions)
	}
	assertBrowserNote(t, a, task, "Chrome")
	if len(task.Revisions) == 0 || task.Failures != 0 {
		t.Fatalf("the task should have its draft without a failure: %+v", task)
	}
}

func assertBrowserNote(t *testing.T, lp *Loop, task core.Task, reason string) {
	t.Helper()
	steps, err := lp.Core.TurnSteps(context.Background(), task.ProjectID, task.ID, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Kind == core.StepNote && strings.Contains(step.Text, reason) {
			return
		}
	}
	t.Fatalf("no browser note containing %q: %+v", reason, steps)
}

func TestCodexMissingBridgeFallsBackAndRecordsWhy(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, fail: []error{&session.CapabilityError{Engine: harness.Codex, Code: session.CapabilityBrowserBridgeUnavailable, Reason: session.BridgeNotDeclared}}}
	lp, member := browserTeamOnEngine(t, runner, "codex")
	_, err := lp.Core.SaveMember(context.Background(), member.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "codex", Browser: core.Browser{On: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := lp.Config()
	cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
	lp.Config = func() config.Config { return cfg }
	task := settle(t, lp)
	writers := writerTurnsOf(runner)
	if len(writers) < 2 {
		t.Fatal("no fallback")
	}
	first, retry := runner.seen[writers[0]], runner.seen[writers[1]]
	if !first.Browser || first.BridgeHome != cfg.Engines.BridgeHome("codex") || retry.Browser || retry.BridgeHome != "" {
		t.Fatalf("first %+v retry %+v", first, retry)
	}
	if retry.Write != first.Write || retry.Loopback != first.Loopback || retry.Web != first.Web || !slices.Equal(retry.Read, first.Read) || !slices.Equal(retry.Env, first.Env) || !strings.Contains(retry.Instructions, noBrowserNote) || !strings.Contains(retry.Instructions, "declared") {
		t.Fatal(retry)
	}
	if len(task.Revisions) == 0 || task.Failures != 0 {
		t.Fatal(task)
	}
	assertBrowserNote(t, lp, task, "declared")
}

func TestBrowserDoesNotRetryNonBrowserError(t *testing.T) {
	failure := errors.New("ordinary failure")
	runner := &scriptedRunner{fail: []error{failure}}
	lp, _ := browserTeam(t, runner)
	_, err := lp.runRole(context.Background(), roles.Spec{Engine: "claude", Browser: true})
	if !errors.Is(err, failure) || len(runner.seen) != 1 {
		t.Fatalf("%v, %d runs", err, len(runner.seen))
	}
}

func TestCodexBaseSpecBridgeHome(t *testing.T) {
	lp, member := browserTeam(t, &scriptedRunner{})
	_, err := lp.Core.SaveMember(context.Background(), member.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "codex", Browser: core.Browser{On: true}})
	if err != nil {
		t.Fatal(err)
	}
	seat := core.Role{Name: "Ada", Engine: "codex", Member: member.ID}
	for _, home := range []string{"", t.TempDir()} {
		cfg := lp.Config()
		cfg.Engines.Codex.BrowserBridgeHome = home
		lp.Config = func() config.Config { return cfg }
		spec := lp.baseSpec(seat, t.TempDir(), "go")
		if !spec.Browser || spec.BridgeHome != cfg.Engines.BridgeHome("codex") {
			t.Fatal(spec)
		}
	}
}

// QA can add the browser after baseSpec; runRole must fill its bridge home.
func TestCodexQABrowserGetsBridgeHomeAtLaunch(t *testing.T) {
	for _, configured := range []bool{false, true} {
		runner := &scriptedRunner{reviews: []string{pass}}
		lp, _ := browserTeamOnEngine(t, runner, "codex")
		cfg := lp.Config()
		if configured {
			cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
		}
		lp.Config = func() config.Config { return cfg }
		spec := roles.Spec{Engine: "codex", WorkDir: t.TempDir()}
		app := appRun{recipe: &core.RunRecipe{Start: "npm start", URL: "http://localhost:{port}/"}, port: 43001, browser: core.Browser{On: true}}
		app.apply(&spec)
		if _, err := lp.runRole(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
		if len(runner.seen) != 1 || !runner.seen[0].Browser || runner.seen[0].BridgeHome != cfg.Engines.BridgeHome("codex") {
			t.Fatalf("QA browser launch: %+v", runner.seen)
		}
	}
}

// Exercise the real read-only CLI bridge check before the fake model turn.
type bridgeCheckingRunner struct{ *scriptedRunner }

func (r bridgeCheckingRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if spec.Browser && spec.Engine == "codex" {
		if err := roles.CheckBrowserBridge(ctx, spec); err != nil {
			return roles.Result{}, err
		}
	}
	return r.scriptedRunner.Run(ctx, spec)
}

func TestFakeCodexWithNoBridgeStillCompletesTurn(t *testing.T) {
	binary, owner, crew := testbridge.Fixture(t)
	if err := os.Remove(filepath.Join(owner, "bridge.json")); err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{reviews: []string{pass}}
	lp, member := browserTeamOnEngine(t, runner, "codex")
	_, err := lp.Core.SaveMember(context.Background(), member.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "codex", Browser: core.Browser{On: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := lp.Config()
	cfg.Engines.Codex.Bin = binary
	cfg.Engines.Codex.Home = crew
	cfg.Engines.Codex.BrowserBridgeHome = t.TempDir()
	lp.Config = func() config.Config { return cfg }
	lp.runner = bridgeCheckingRunner{runner}
	task := settle(t, lp)
	if len(task.Revisions) == 0 || task.Failures != 0 {
		t.Fatal(task)
	}
	assertBrowserNote(t, lp, task, "declared")
	for _, i := range writerTurnsOf(runner) {
		if runner.seen[i].Browser || runner.seen[i].BridgeHome != "" {
			t.Fatal("browser retained in fallback")
		}
	}
}

func TestQABrowserFallbackNoteSurvivesScreenshotObserver(t *testing.T) {
	lp := testLoop(t)
	watch := lp.watchTurn(core.Task{ID: "task"}, core.RoleQA, core.Role{Name: "QA"}, t.TempDir(), false).(*liveTurn)
	var kept []core.TurnStep
	watch.steps.keep = func(s core.TurnStep) { kept = append(kept, s) }
	observer := browserFallbackObserver{Observer: &screenshots{next: watch}, note: "Ran without the browser. Chrome is not connected."}
	if len(kept) != 0 {
		t.Fatal("note before start")
	}
	observer.Started()
	observer.Ended()
	if len(kept) != 1 || kept[0].Kind != core.StepNote || kept[0].Text != observer.note {
		t.Fatal(kept)
	}
}
