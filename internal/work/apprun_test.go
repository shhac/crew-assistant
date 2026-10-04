//go:build !windows

package work

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/testutil"
	harness "github.com/shhac/lib-agent-harness"
)

// appRunner is a code team whose QA, while it checks, reports what its
// tools returned as a real session does, screenshots included, and replies
// qaReply. The reviewer checks the same draft at the same time, so QA's
// reply is its own rather than the next in the shared list.
type appRunner struct {
	codeRunner
	events  []session.Event
	qaReply string
	qa      []roles.Spec
	// built is what QA's app commands could write, as setup would: whether
	// a file could be made at the root the prompt names for them, and in the
	// read-only checkout, tried while the check's copy still exists.
	built []appTree
}

type appTree struct {
	root               string
	rootOK, checkoutOK bool
}

// appCommandsRoot is where QA's prompt says to run the app's commands, or "".
func appCommandsRoot(prompt string) string {
	_, rest, ok := strings.Cut(prompt, "The app builds in ")
	if !ok {
		return ""
	}
	root, _, _ := strings.Cut(rest, ",")
	return root
}

func canWrite(dir string) bool {
	f, err := os.CreateTemp(dir, "build-output-")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func (r *appRunner) Run(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if !strings.Contains(spec.Prompt, "Use run_check for the project check") {
		return r.codeRunner.Run(ctx, spec)
	}
	tree := appTree{root: appCommandsRoot(spec.Prompt)}
	if tree.root != "" {
		tree.rootOK = canWrite(tree.root)
	}
	if n := len(spec.Read); n > 0 {
		tree.checkoutOK = canWrite(spec.Read[n-1])
	}
	r.mu.Lock()
	r.built = append(r.built, tree)
	r.mu.Unlock()
	if spec.Observer != nil {
		spec.Observer.Started()
		for _, e := range r.events {
			spec.Observer.Saw(e)
		}
		spec.Observer.Ended()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.qa = append(r.qa, spec)
	r.seen = append(r.seen, spec)
	return roles.Result{Text: r.qaReply}, nil
}

func screenshot(t *testing.T, width int) session.Image {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, width, 2))); err != nil {
		t.Fatal(err)
	}
	return session.Image{MediaType: "image/png", Data: b.Bytes()}
}

const qaSawTheApp = "```json\n" + `{"outcome":"pass","summary":"The check passes and the page works.","findings":[],"question":"","evidence":[{"kind":"console","text":"No errors in the console."},{"kind":"network","text":"GET / answered 200."},{"kind":"screenshot","text":"made up"},{"kind":"page","text":""}]}` + "\n```"

var testRecipe = core.RunRecipe{Setup: "npm run build", Start: "npm start", URL: "http://127.0.0.1:{port}/", Ready: "curl -sf http://127.0.0.1:{port}/health"}

// qaTeam is a code project whose QA is a member on engine with the browser
// setting given, and with the run recipe given, if any.
func qaTeam(t *testing.T, runner *appRunner, engine string, browser core.Browser, recipe *core.RunRecipe) (*Loop, core.Project) {
	t.Helper()
	a, _, _ := loopApp(t, &runner.scriptedRunner, "")
	a.runner = runner
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{started: newFakeStarted()}, nil
	}
	next := 43000
	a.ports.find = func() (int, error) { next++; return next, nil }
	ctx := context.Background()
	p := codeProject(t, a, ownerRepo(t))
	m, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Quinn", Kinds: []string{core.RoleQA}, Engine: engine, Browser: browser})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetSeat(ctx, p.ID, core.RoleQA, m.ID); err != nil {
		t.Fatal(err)
	}
	if recipe != nil {
		if p, err = a.SetRunRecipe(ctx, p.ID, recipe); err != nil {
			t.Fatal(err)
		}
	}
	return a, p
}

func verdictBy(task core.Task, role string) core.Verdict {
	for _, v := range task.Verdicts {
		if v.Role == role {
			return v
		}
	}
	return core.Verdict{}
}

func portOf(spec roles.Spec) string {
	for _, e := range spec.Env {
		if port, ok := strings.CutPrefix(e, "PORT="); ok {
			return port
		}
	}
	return ""
}

// Claude QA on a project with a run recipe starts the app on a port of its
// own, reaches it on this machine only, and uses the browser its seat names;
// its screenshots and what it saw are kept with its verdict.
func TestClaudeQARunsTheAppAndKeepsWhatItSaw(t *testing.T) {
	t.Parallel()
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: qaSawTheApp}
	runner.events = []session.Event{
		{Kind: "tool_started", Tool: "mcp__claude-in-chrome__computer"},
		{Kind: "tool_completed", Tool: "mcp__claude-in-chrome__computer", Images: []session.Image{screenshot(t, 1), screenshot(t, 2), screenshot(t, 3)}},
		{Kind: "tool_completed", Tool: "Bash", Output: "ok"},
		{Kind: "tool_completed", Tool: "mcp__claude-in-chrome__computer", Images: []session.Image{screenshot(t, 4), screenshot(t, 5)}, ImagesOmitted: 1},
	}
	a, p := qaTeam(t, runner, "claude", core.Browser{On: true, Name: "Work laptop"}, &testRecipe)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(runner.qa) != 1 {
		t.Fatalf("task %s after %d QA turns", task.Status, len(runner.qa))
	}
	qa := runner.qa[0]
	port := "43001"
	if qa.Loopback || !qa.Browser || !qa.Write || qa.Web || portOf(qa) != "" {
		t.Fatalf("QA was not given the app to run: loopback %v browser %v port %q", qa.Loopback, qa.Browser, port)
	}
	// The app's commands run in a writable copy of the revision in QA's
	// scratch folder, so setup can build. The hosted check gets its own
	// writable copy of the read-only revision.
	built := runner.built[0]
	if built.root == "" || !built.rootOK || !strings.HasPrefix(built.root, qa.WorkDir+string(os.PathSeparator)) || built.checkoutOK {
		t.Fatalf("the app's commands can't build in a copy of their own: %+v, scratch %s", built, qa.WorkDir)
	}
	if !strings.Contains(qa.Prompt, "checked out, read-only, at "+qa.Read[len(qa.Read)-1]+". run_check runs the check in its own writable copy") {
		t.Fatalf("the hosted check no longer names its read-only revision source:\n%s", qa.Prompt)
	}
	for _, want := range []string{"http://127.0.0.1:" + port + "/", ".crew-app.log", "will stop it when your turn ends", `select_browser`, `"Work laptop"`, "real Chrome", `"evidence"`} {
		if !strings.Contains(qa.Prompt, want) {
			t.Errorf("QA's prompt does not say %q", want)
		}
	}
	// Everyone else runs as before.
	for _, spec := range runner.seen {
		if !strings.Contains(spec.Prompt, "Use run_check for the project check") && (spec.Loopback || spec.Browser || portOf(spec) != "") {
			t.Fatalf("a role other than QA was given the app: %+v", spec)
		}
	}
	// The port was held only while the check ran.
	if len(a.ports.held) != 0 {
		t.Fatalf("ports still held: %v", a.ports.held)
	}
	v := verdictBy(task, "Quinn")
	var shots, words []core.Evidence
	for _, e := range v.Evidence {
		if e.Kind == core.EvidenceScreenshot {
			shots = append(shots, e)
		} else {
			words = append(words, e)
		}
	}
	if len(words) != 2 || words[0] != (core.Evidence{Kind: core.EvidenceConsole, Text: "No errors in the console."}) || words[1].Kind != core.EvidenceNetwork {
		t.Fatalf("what QA saw: %+v", words)
	}
	// The last four screenshots are kept, and the rest are counted.
	if v.ID == "" || len(shots) != core.MaxScreenshots+1 || shots[core.MaxScreenshots].Attachment != "" || !strings.Contains(shots[core.MaxScreenshots].Text, "2 more screenshots") {
		t.Fatalf("screenshots %+v", shots)
	}
	for i, e := range shots[:core.MaxScreenshots] {
		at := slices.IndexFunc(task.Attachments, func(a core.Attachment) bool { return a.ID == e.Attachment })
		if at < 0 {
			t.Fatalf("screenshot %d is not kept with the task", i)
		}
		kept := task.Attachments[at]
		want := screenshot(t, i+2).Data
		if kept.Verdict != v.ID || kept.Type != "image/png" || kept.By != "Quinn" || kept.Size != int64(len(want)) {
			t.Fatalf("screenshot %d kept as %+v", i, kept)
		}
		_, path, err := a.Core.OpenAttachment(context.Background(), p.ID, task.ID, kept.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, want) {
			t.Fatalf("screenshot %d holds other bytes", i)
		}
	}
	// The researcher was offered a way to propose a recipe; QA was not.
	var researcher roles.Spec
	for _, spec := range runner.seen {
		if strings.Contains(spec.Prompt, "Plan this task before anything is written") {
			researcher = spec
		}
	}
	if !hasTool(researcher, "propose_run_recipe") || hasTool(qa, "propose_run_recipe") {
		t.Fatal("proposing a recipe was offered to the wrong roles")
	}
	// Whoever works on the task next reads what QA saw.
	if h := historyText(task, true); !strings.Contains(h, "saw (console): No errors in the console.") || !strings.Contains(h, "screenshot kept with the task: screenshot-1.png") {
		t.Fatalf("history %s", h)
	}
}

// keptOnDisk lists the files kept for a task's attachments, if any.
func keptOnDisk(t *testing.T, a *Loop, taskID string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(a.Core.AttachmentsDirectory(taskID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return entries
}

// The owner stops the task once QA has used the app and taken screenshots,
// before its verdict is recorded: nothing of the check is kept, so no
// screenshot names a verdict that never was.
func TestStoppingATaskBeforeQAsVerdictKeepsNoScreenshots(t *testing.T) {
	t.Parallel()
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: qaSawTheApp}
	runner.events = []session.Event{{Kind: "tool_completed", Tool: "mcp__claude-in-chrome__computer", Images: []session.Image{screenshot(t, 1), screenshot(t, 2)}}}
	a, p := qaTeam(t, runner, "claude", core.Browser{On: true}, &testRecipe)
	a.checked = func(taskID, checker string) {
		if checker != "Quinn" {
			return
		}
		if _, err := a.StopTask(context.Background(), p.ID, taskID); err != nil {
			t.Error(err)
		}
	}
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskStopped || len(runner.qa) != 1 {
		t.Fatalf("task %s after %d QA turns", task.Status, len(runner.qa))
	}
	if len(task.Attachments) != 0 || verdictBy(task, "Quinn").Role != "" {
		t.Fatalf("kept after the stop: attachments %+v, verdicts %+v", task.Attachments, task.Verdicts)
	}
	if files := keptOnDisk(t, a, task.ID); len(files) != 0 {
		t.Fatalf("files left: %d", len(files))
	}
}

// Asked directly once approval is waiting, QA's passing check is not
// counted, so the screenshots it took are not kept either.
func TestQAsAnswerThatDoesNotCountKeepsNoScreenshots(t *testing.T) {
	t.Parallel()
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: qaSawTheApp}
	runner.events = []session.Event{{Kind: "tool_completed", Tool: "mcp__claude-in-chrome__computer", Images: []session.Image{screenshot(t, 1), screenshot(t, 2)}}}
	a, p := qaTeam(t, runner, "claude", core.Browser{On: true}, &testRecipe)
	ctx := context.Background()
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	if task.Status != core.TaskWaiting || len(task.Attachments) != 2 || len(keptOnDisk(t, a, task.ID)) != 2 {
		t.Fatalf("task %s with attachments %+v", task.Status, task.Attachments)
	}
	if _, err := a.MessageTeam(ctx, p.ID, task.ID, "Quinn", core.FromOwner, "Look again"); err != nil {
		t.Fatal(err)
	}
	task = settleCode(t, a, task.ID)
	if len(runner.qa) != 2 || task.Messages[0].Status != core.MessageAnswered || len(task.Verdicts) != 2 {
		t.Fatalf("%d QA turns, message %+v, verdicts %d", len(runner.qa), task.Messages[0], len(task.Verdicts))
	}
	for _, a := range task.Attachments {
		if !slices.ContainsFunc(task.Verdicts, func(v core.Verdict) bool { return v.ID == a.Verdict }) {
			t.Fatalf("attachment %+v names no verdict", a)
		}
	}
	if len(task.Attachments) != 2 || len(keptOnDisk(t, a, task.ID)) != 2 {
		t.Fatalf("attachments %+v", task.Attachments)
	}
}

func hasTool(spec roles.Spec, name string) bool {
	return slices.ContainsFunc(spec.Tools, func(d session.ToolDefinition) bool { return d.Name == name })
}

// Codex QA without an allowed browser uses the hosted check alone and
// reports why the daemon-hosted app is unavailable.
func TestCodexQASaysWhyItDidNotRunTheApp(t *testing.T) {
	t.Parallel()
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: pass}
	runner.events = []session.Event{{Kind: "tool_completed", Tool: "x", Images: []session.Image{screenshot(t, 1)}}}
	a, p := qaTeam(t, runner, "codex", core.Browser{}, &testRecipe)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	qa := runner.qa[0]
	if qa.Loopback || qa.Browser || qa.Web || portOf(qa) != "" || strings.Contains(qa.Prompt, "npm start") {
		t.Fatalf("Codex QA was given the app to run: %+v", qa)
	}
	if built := runner.built[0]; built.root != "" || strings.Contains(qa.Prompt, "The app builds in") {
		t.Fatalf("Codex QA without a browser got an app tree: %+v", built)
	}
	v := verdictBy(task, "Quinn")
	if len(v.Findings) != 1 || v.Findings[0].Criterion != "Running the app" || !strings.Contains(v.Findings[0].Note, "Codex") || !strings.Contains(v.Findings[0].Note, "needs an allowed browser") {
		t.Fatalf("Codex QA's verdict %+v", v)
	}
	if len(v.Evidence) != 0 || len(task.Attachments) != 0 {
		t.Fatalf("evidence from a check that did not run the app: %+v %+v", v.Evidence, task.Attachments)
	}
}

// Without a run recipe, QA gets the hosted check and no app guidance,
// even with its browser setting on.
func TestQAWithoutARecipeRunsTheCheckAsBefore(t *testing.T) {
	t.Parallel()
	runner := &appRunner{codeRunner: codeRunner{scriptedRunner: scriptedRunner{reviews: []string{pass}}}, qaReply: pass}
	runner.events = []session.Event{{Kind: "tool_completed", Tool: "x", Images: []session.Image{screenshot(t, 1)}}}
	a, p := qaTeam(t, runner, "claude", core.Browser{On: true}, nil)
	task, _ := a.Core.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Add Feature"})
	task = settleCode(t, a, task.ID)
	qa := runner.qa[0]
	if qa.Loopback || qa.Web || portOf(qa) != "" || strings.Contains(qa.Prompt, "use the app") || strings.Contains(qa.Prompt, "evidence") {
		t.Fatalf("QA without a recipe was given the app: %+v", qa)
	}
	// Its member allows the browser, so it has one all the same, for looking.
	if !qa.Browser || !strings.Contains(qa.Instructions, "only for controlling a browser") {
		t.Fatalf("QA whose member allows the browser: %+v", qa)
	}
	// QA gets no app tree: run_check copies the read-only
	// checkout, exactly as before.
	if built := runner.built[0]; built.root != "" || built.checkoutOK || strings.Contains(qa.Prompt, "The app builds in") || !strings.Contains(qa.Prompt, "checked out, read-only, at") {
		t.Fatalf("QA without a recipe got an app tree: %+v\n%s", built, qa.Prompt)
	}
	if entries, _ := os.ReadDir(qa.WorkDir); len(entries) != 0 {
		t.Fatalf("the check's scratch folder was left behind: %v", entries)
	}
	if v := verdictBy(task, "Quinn"); len(v.Findings) != 0 || len(v.Evidence) != 0 || v.ID != "" || len(task.Attachments) != 0 {
		t.Fatalf("verdict %+v, attachments %+v", v, task.Attachments)
	}
}

// Checks running side by side never share a port, and a port is free again
// once the check that held it is done.
func TestChecksSideBySideNeverShareAPort(t *testing.T) {
	t.Parallel()
	var p ports
	offered := []int{41000, 41000, 41000, 41001}
	p.find = func() (int, error) {
		port := offered[0]
		offered = offered[1:]
		return port, nil
	}
	first, err := p.reserve()
	if err != nil || first != 41000 {
		t.Fatalf("first %d %v", first, err)
	}
	second, err := p.reserve()
	if err != nil || second != 41001 {
		t.Fatalf("a second check was given %d (%v) while the first held %d", second, err, first)
	}
	p.free(first)
	offered = []int{41000}
	if again, err := p.reserve(); err != nil || again != 41000 {
		t.Fatalf("a freed port was not given out again: %d %v", again, err)
	}

	lp := testLoop(t)
	qa := core.Role{Name: "QA", Kinds: []string{core.RoleQA}, Engine: "claude"}
	playbook := &core.Playbook{Medium: core.MediumGit, Run: &testRecipe}
	testutil.RequireLoopback(t)
	one, err := lp.planApp(qa, playbook)
	if err != nil {
		t.Fatal(err)
	}
	two, err := lp.planApp(qa, playbook)
	if err != nil {
		t.Fatal(err)
	}
	if one.port == 0 || one.port == two.port {
		t.Fatalf("two checks were given ports %d and %d", one.port, two.port)
	}
	one.release()
	two.release()
	if len(lp.ports.held) != 0 {
		t.Fatalf("held %v", lp.ports.held)
	}
}

// The recipe is set with the team or on its own, kept when the team is
// chosen again, and taken away only when asked; QA's browser is copied from
// its member and can be changed for the project, on an engine that has one.
func TestTheRecipeAndQAsBrowserAreProjectSettings(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	ctx := context.Background()
	source := ownerRepo(t)
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Directories: []string{source}, Brief: core.BriefInput{Goal: "Add features"}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make check", Run: &core.RunRecipe{Start: " npm start ", URL: "http://localhost:{port}/"}}); err != nil {
		t.Fatal(err)
	}
	if p.Playbook.Run == nil || *p.Playbook.Run != (core.RunRecipe{Start: "npm start", URL: "http://localhost:{port}/"}) {
		t.Fatalf("recipe with the team %+v", p.Playbook.Run)
	}
	if p, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make test"}); err != nil || p.Playbook.Run == nil {
		t.Fatalf("choosing the team again dropped the recipe: %+v %v", p.Playbook, err)
	}
	if _, err = a.SetTeam(ctx, p.ID, TeamChoice{Template: "code", Check: "make test", Run: &core.RunRecipe{Start: "npm start", URL: "http://example.com:{port}/"}}); err == nil {
		t.Fatal("a recipe off this machine was set with the team")
	}
	if p, err = a.SetRunRecipe(ctx, p.ID, &testRecipe); err != nil || *p.Playbook.Run != testRecipe {
		t.Fatalf("setting the recipe: %+v %v", p.Playbook.Run, err)
	}
	if p, err = a.SetRunRecipe(ctx, p.ID, nil); err != nil || p.Playbook.Run != nil {
		t.Fatalf("taking the recipe away: %+v %v", p.Playbook.Run, err)
	}
	// The template's QA is on Codex, whose sandboxed sessions admit the
	// browser too.
	if p, err = a.SetSeatBrowser(ctx, p.ID, core.Browser{On: true}); err != nil {
		t.Fatalf("Codex QA wasn't given the browser: %v", err)
	}
	if _, err = a.SetSeatBrowser(ctx, p.ID, core.Browser{}); err != nil {
		t.Fatal(err)
	}
	m, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Quinn", Kinds: []string{core.RoleQA, core.RoleResearcher}, Engine: "claude", Browser: core.Browser{On: true, Name: "Home"}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = a.SetSeat(ctx, p.ID, core.RoleQA, m.ID); err != nil {
		t.Fatal(err)
	}
	qa, _ := seatHolding(p, core.RoleQA)
	if qa.Browser != (core.Browser{On: true, Name: "Home"}) {
		t.Fatalf("the member's browser setting did not come with them: %+v", qa)
	}
	if p, err = a.SetSeatBrowser(ctx, p.ID, core.Browser{On: true, Name: " "}); err != nil {
		t.Fatal(err)
	}
	if qa, _ = seatHolding(p, core.RoleQA); qa.Browser != (core.Browser{On: true}) {
		t.Fatalf("the project's own setting: %+v", qa)
	}
	// Loopback for the check changes only that setting, never the seats.
	extra, err := a.AddToRole(ctx, p.ID, core.RoleReviewer, "")
	if err != nil {
		t.Fatal(err)
	}
	seats := len(extra.Playbook.Roles)
	if p, err = a.SetCheckLoopback(ctx, p.ID, true); err != nil || !p.Playbook.CheckLoopback || len(p.Playbook.Roles) != seats {
		t.Fatalf("turning check loopback on: %+v %v", p.Playbook, err)
	}
	if p, err = a.SetCheckLoopback(ctx, p.ID, false); err != nil || p.Playbook.CheckLoopback || len(p.Playbook.Roles) != seats {
		t.Fatalf("turning check loopback off: %+v %v", p.Playbook, err)
	}
	// So does the check command, and a team with QA keeps one.
	if p, err = a.SetCheck(ctx, p.ID, "  make check lint  "); err != nil || p.Playbook.Check != "make check lint" || len(p.Playbook.Roles) != seats {
		t.Fatalf("setting the check: %+v %v", p.Playbook, err)
	}
	if _, err = a.SetCheck(ctx, p.ID, " "); err == nil {
		t.Fatal("a team with QA accepted an empty check")
	}
}

func seatHolding(p core.Project, kind string) (core.Role, bool) {
	for _, r := range p.Playbook.Roles {
		if r.Holds(kind) {
			return r, true
		}
	}
	return core.Role{}, false
}

// The researcher and the PM of a code project can propose a run recipe for
// the owner to accept; it changes nothing until they do.
func TestTheResearcherAndThePMProposeARecipe(t *testing.T) {
	t.Parallel()
	a := testLoop(t)
	ctx := context.Background()
	id := codeProject(t, a, ownerRepo(t)).ID
	snap, _ := a.Core.Snapshot(ctx)
	p, _ := findProject(snap, id)
	task, _ := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Add Feature"})
	researcher := a.toolsFor(task, core.RoleResearcher, core.Role{Name: "Researcher"}).proposing(p.Playbook)
	args := `{"setup":"","start":"npm start","url":"http://127.0.0.1:{port}/","ready":"","why":"package.json starts it"}`
	if out, err := researcher.call(ctx, "propose_run_recipe", []byte(args)); err != nil || !strings.Contains(out, "owner decides") {
		t.Fatalf("proposing: %q %v", out, err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	i := slices.IndexFunc(snap.Decisions, func(d core.Decision) bool { return d.Kind == core.DecisionRunRecipe })
	if i < 0 || snap.Decisions[i].Run == nil || !strings.Contains(snap.Decisions[i].Title, "Researcher") {
		t.Fatalf("no proposal for the owner: %+v", snap.Decisions)
	}
	if now, _ := findProject(snap, p.ID); now.Playbook.Run != nil {
		t.Fatal("the proposal applied before the owner accepted it")
	}
	if _, err := a.Core.ChooseDecision(ctx, snap.Decisions[i].ID, core.ChoiceUseRecipe, core.FromOwner); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Core.Snapshot(ctx)
	if now, _ := findProject(snap, p.ID); now.Playbook.Run == nil || now.Playbook.Run.Start != "npm start" {
		t.Fatal("the accepted recipe was not set")
	}

	pm := a.managerTools(p.ID, core.Role{Name: "Pim"}).proposing(p.Playbook)
	if !slices.ContainsFunc(pm.Definitions(), func(d session.ToolDefinition) bool { return d.Name == "propose_run_recipe" }) || !strings.Contains(pm.guide(), "propose_run_recipe") {
		t.Fatal("the PM can't propose a recipe")
	}
	if _, err := pm.call(ctx, "propose_run_recipe", []byte(`{"setup":"","start":"npm start","url":"https://example.com/","ready":"","why":""}`)); err == nil {
		t.Fatal("the PM proposed a recipe off this machine")
	}
	for name, tools := range map[string]roleTools{
		"a reviewer":         a.toolsFor(task, core.RoleReviewer, core.Role{Name: "Reviewer"}),
		"the PM answering":   a.answerTools(p.ID, core.Role{Name: "Pim"}).proposing(p.Playbook),
		"a writing team's":   a.managerTools(p.ID, core.Role{Name: "Pim"}).proposing(&core.Playbook{Medium: core.MediumDocuments}),
		"a researcher alone": a.toolsFor(task, core.RoleResearcher, core.Role{Name: "Researcher"}),
	} {
		if slices.ContainsFunc(tools.Definitions(), func(d session.ToolDefinition) bool { return d.Name == "propose_run_recipe" }) {
			t.Errorf("%s tools offer proposing a recipe", name)
		}
		if _, err := tools.call(ctx, "propose_run_recipe", []byte(args)); err == nil {
			t.Errorf("%s tools proposed a recipe", name)
		}
	}
}

// QA selects a browser only when one is named. With no name there is nothing
// to select: it uses the browser the extension connects by default, and is
// told not to switch.
func TestQASelectsABrowserOnlyWhenOneIsNamed(t *testing.T) {
	t.Parallel()
	app := appRun{recipe: &testRecipe, port: 41234, browser: core.Browser{On: true}}
	prompt := appPrompt(app)
	if strings.Contains(prompt, "select_browser") || !strings.Contains(prompt, "connects by default; don't select or switch") {
		t.Fatalf("with no browser named:\n%s", prompt)
	}
	app.browser.Name = "Work laptop"
	if prompt = appPrompt(app); !strings.Contains(prompt, `call select_browser to choose the connected browser named "Work laptop"`) {
		t.Fatalf("with a browser named:\n%s", prompt)
	}
	app.browser = core.Browser{}
	if prompt = appPrompt(app); strings.Contains(prompt, "select_browser") || strings.Contains(prompt, "Chrome") {
		t.Fatalf("with the browser off:\n%s", prompt)
	}
}

func TestQAIsToldItsPortIsAlreadySet(t *testing.T) {
	t.Parallel()
	app := appRun{recipe: &testRecipe, port: 41234}
	spec := roles.Spec{}
	app.apply(&spec)
	prompt := appPrompt(app)
	if portOf(spec) != "" || strings.Contains(prompt, testRecipe.Start) || !strings.Contains(prompt, "will stop it when your turn ends") || !strings.Contains(prompt, testRecipe.Address(app.port)) {
		t.Fatalf("%+v %s", spec, prompt)
	}
}

// Which engines run the app, and how, comes from what the harness says each
// offers, never from the engine's name.
func TestHowQARunsTheAppFollowsWhatTheHarnessOffers(t *testing.T) {
	t.Parallel()
	lp := testLoop(t)
	lp.ports.find = func() (int, error) { return 42000, nil }
	playbook := &core.Playbook{Medium: core.MediumGit, Run: &testRecipe}
	for _, c := range []struct {
		role                    core.Role
		running, browser, shots bool
		unavailable             bool
	}{
		{role: core.Role{Kinds: []string{core.RoleQA}, Engine: "claude", Browser: core.Browser{On: true}}, running: true, browser: true, shots: true},
		{role: core.Role{Kinds: []string{core.RoleQA}, Engine: "claude"}, running: true, shots: true},
		{role: core.Role{Kinds: []string{core.RoleQA}, Engine: "codex", Browser: core.Browser{On: true}}, running: true, browser: true, shots: true},
		{role: core.Role{Kinds: []string{core.RoleReviewer}, Engine: "claude"}},
	} {
		app, err := lp.planApp(c.role, playbook)
		if err != nil {
			t.Fatal(err)
		}
		spec := roles.Spec{Engine: c.role.Engine, Env: []string{"GOCACHE=/x"}}
		app.apply(&spec)
		if app.running() != c.running || spec.Loopback != (c.running && !c.browser) || spec.Browser != c.browser || app.images != c.shots || (app.unavailable != "") != c.unavailable {
			t.Errorf("%s %v: app %+v spec %+v", c.role.Engine, c.role.Kinds, app, spec)
		}
		if c.running && !slices.Equal(spec.Env, []string{"GOCACHE=/x"}) {
			t.Errorf("env %v", spec.Env)
		}
		app.release()
	}
	app, _ := lp.planApp(core.Role{Kinds: []string{core.RoleQA}, Engine: "claude"}, &core.Playbook{Medium: core.MediumGit})
	if app.running() || app.unavailable != "" || appPrompt(app) != "" {
		t.Fatalf("no recipe: %+v", app)
	}
}

func TestAPIQAAppRunFollowsLoopbackSupportAndCarriesPort(t *testing.T) {
	t.Parallel()
	lp := testLoop(t)
	reserved := 0
	lp.ports.find = func() (int, error) { reserved++; return 42000, nil }
	app, err := lp.planApp(core.Role{Kinds: []string{core.RoleQA}, Engine: "openai-compatible"}, &core.Playbook{Run: &testRecipe})
	if err != nil {
		t.Fatal(err)
	}
	defer app.release()
	spec := roles.Spec{Env: []string{"GOCACHE=/cache", "GOPROXY=off"}}
	app.apply(&spec)
	if config.Supports("openai-compatible", config.UseLoopback) {
		if !app.running() || reserved != 1 || !spec.Loopback || !slices.Equal(spec.Env, []string{"GOCACHE=/cache", "GOPROXY=off"}) {
			t.Fatalf("%+v %+v", app, spec)
		}
		prompt := appPrompt(app)
		if !strings.Contains(prompt, testRecipe.Address(42000)) || !strings.Contains(prompt, "will stop it when your turn ends") || spec.Browser {
			t.Fatalf("%+v %s", spec, prompt)
		}
	} else {
		reason := harness.Support(harness.OpenAICompatible, harness.Session, harness.Loopback).Reason
		if app.running() || reserved != 0 || spec.Loopback || !strings.Contains(app.unavailable, reason) || !strings.Contains(appPrompt(app), app.unavailable) {
			t.Fatalf("%+v %+v", app, spec)
		}
	}
}
