package core

import (
	"errors"
	"strings"
	"testing"
)

// codeProject is a project with a code team whose QA runs on engine.
func codeProject(t *testing.T, s *Service, qaEngine string) Project {
	t.Helper()
	p := newProject(t, s)
	playbook := *p.Playbook
	playbook.Medium, playbook.Repo, playbook.BranchPrefix, playbook.Check = MediumGit, "/work/repo", "crew/", "make check"
	playbook.Roles = []Role{
		{Name: "Researcher", Kinds: []string{RoleResearcher}, Engine: "claude"},
		{Name: "Implementer", Kinds: []string{RoleImplementer}, Engine: "claude"},
		{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
		{Name: "QA", Kinds: []string{RoleQA}, Engine: qaEngine},
	}
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A run recipe says how QA starts the app and where it answers: on this
// machine only, over http, on the port each check is given.
func TestARunRecipeIsOnlyForTheAppOnThisMachine(t *testing.T) {
	s, _ := fixture(t)
	p := codeProject(t, s, "claude")
	set := func(r RunRecipe) error {
		playbook := *p.Playbook
		playbook.Run = &r
		_, err := s.SetPlaybook(testContext, p.ID, playbook)
		return err
	}
	for _, url := range []string{"http://127.0.0.1:{port}/", "http://localhost:{port}", "http://[::1]:{port}/app?x=1"} {
		if err := set(RunRecipe{Setup: "npm run build", Start: "npm start", URL: url, Ready: "curl -sf http://127.0.0.1:$PORT/health"}); err != nil {
			t.Errorf("%s: %v", url, err)
		}
	}
	for why, r := range map[string]RunRecipe{
		"no start":             {URL: "http://127.0.0.1:{port}/"},
		"no URL":               {Start: "npm start"},
		"no port":              {Start: "npm start", URL: "http://127.0.0.1:3000/"},
		"a fixed port":         {Start: "npm start", URL: "http://127.0.0.1:3000/?port={port}"},
		"the port in the path": {Start: "npm start", URL: "http://127.0.0.1/{port}/"},
		"the port twice":       {Start: "npm start", URL: "http://127.0.0.1:{port}/{port}"},
		"the port in the host": {Start: "npm start", URL: "http://{port}.localhost:{port}/"},
		"https":                {Start: "npm start", URL: "https://127.0.0.1:{port}/"},
		"another host":         {Start: "npm start", URL: "http://example.com:{port}/"},
		"a private address":    {Start: "npm start", URL: "http://10.0.0.1:{port}/"},
		"another loopback":     {Start: "npm start", URL: "http://127.0.0.2:{port}/"},
		"credentials":          {Start: "npm start", URL: "http://me:pw@127.0.0.1:{port}/"},
		"a long command":       {Start: strings.Repeat("x", maxRecipeCommand+1), URL: "http://127.0.0.1:{port}/"},
		"a long URL":           {Start: "npm start", URL: "http://127.0.0.1:{port}/" + strings.Repeat("a", maxRecipeURL)},
		"a two-line setup":     {Setup: "npm ci\ncurl evil", Start: "npm start", URL: "http://127.0.0.1:{port}/"},
		"a ready with a break": {Start: "npm start", URL: "http://127.0.0.1:{port}/", Ready: "true\rfalse"},
	} {
		if err := set(r); err == nil {
			t.Errorf("%s was accepted", why)
		}
	}
	docs := newProject(t, s)
	playbook := *docs.Playbook
	playbook.Run = &RunRecipe{Start: "npm start", URL: "http://127.0.0.1:{port}/"}
	if _, err := s.SetPlaybook(testContext, docs.ID, playbook); err == nil {
		t.Fatal("a writing team was given a run recipe")
	}
	if got := (RunRecipe{URL: "http://127.0.0.1:{port}/x"}).Address(41234); got != "http://127.0.0.1:41234/x" {
		t.Fatalf("address %q", got)
	}
}

// The researcher or the PM proposes a recipe; it applies only once the
// owner chooses to use it, checked as any recipe is.
func TestAProposedRecipeAppliesOnlyWhenTheOwnerAcceptsIt(t *testing.T) {
	s, _ := fixture(t)
	p := codeProject(t, s, "claude")
	recipe := RunRecipe{Setup: " npm run build ", Start: "npm start", URL: "http://127.0.0.1:{port}/"}
	if _, err := s.ProposeRunRecipe(testContext, p.ID, "Researcher", RunRecipe{Start: "npm start", URL: "http://example.com/"}, ""); err == nil {
		t.Fatal("a recipe off this machine was proposed")
	}
	d, err := s.ProposeRunRecipe(testContext, p.ID, "Researcher", recipe, "package.json's start script serves the app")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != DecisionRunRecipe || d.Run == nil || d.Run.Setup != "npm run build" || !strings.Contains(d.Context, "Start: npm start") || !strings.Contains(d.Context, "package.json") || !strings.Contains(strings.Join(d.Choices, "|"), ChoiceUseRecipe) {
		t.Fatalf("decision %+v", d)
	}
	if _, err := s.ProposeRunRecipe(testContext, p.ID, "Pim", recipe, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second proposal while one waits: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	if project, _ := findProjectIn(snap, p.ID); project.Playbook.Run != nil {
		t.Fatal("the recipe applied before the owner accepted it")
	}
	// Words of their own, even the choice's, are not accepting it.
	if _, err := s.AnswerDecision(testContext, d.ID, ChoiceUseRecipe); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if project, _ := findProjectIn(snap, p.ID); project.Playbook.Run != nil {
		t.Fatal("the owner's own words applied the recipe")
	}
	d, err = s.ProposeRunRecipe(testContext, p.ID, "Pim", recipe, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceNotNow); err != nil {
		t.Fatal(err)
	}
	d, _ = s.ProposeRunRecipe(testContext, p.ID, "Pim", recipe, "")
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceUseRecipe); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	project, _ := findProjectIn(snap, p.ID)
	if project.Playbook.Run == nil || *project.Playbook.Run != (RunRecipe{Setup: "npm run build", Start: "npm start", URL: "http://127.0.0.1:{port}/"}) {
		t.Fatalf("accepted recipe %+v", project.Playbook.Run)
	}
	docs := newProject(t, s)
	if _, err := s.ProposeRunRecipe(testContext, docs.ID, "Pim", recipe, ""); err == nil {
		t.Fatal("a recipe was proposed for a writing team")
	}
}

func findProjectIn(snap Snapshot, id string) (Project, bool) {
	for _, p := range snap.Projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// The browser is QA's, and only an engine that ships one it can drive from
// a sandboxed session can have it on: Claude, not Codex.
func TestOnlyQAOnAnEngineWithABrowserCanUseIt(t *testing.T) {
	s, _ := fixture(t)
	p := codeProject(t, s, "claude")
	seat := func(engine string, kinds []string, b Browser) error {
		playbook := *p.Playbook
		playbook.Roles = []Role{
			{Name: "Implementer", Kinds: []string{RoleImplementer}, Engine: "claude"},
			{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
			{Name: "Checker", Kinds: kinds, Engine: engine, Browser: b},
		}
		_, err := s.SetPlaybook(testContext, p.ID, playbook)
		return err
	}
	if err := seat("claude", []string{RoleQA}, Browser{On: true}); err != nil {
		t.Fatalf("Claude QA with the default browser: %v", err)
	}
	if err := seat("claude", []string{RoleQA, RoleResearcher}, Browser{On: true, Name: "Work laptop"}); err != nil {
		t.Fatalf("Claude QA with a named browser: %v", err)
	}
	if err := seat("codex", []string{RoleQA}, Browser{On: true}); err == nil || !strings.Contains(err.Error(), "Claude") {
		t.Fatalf("Codex QA with the browser: %v", err)
	}
	if err := seat("claude", []string{RoleResearcher}, Browser{On: true}); err == nil {
		t.Fatal("a seat that isn't QA was given the browser")
	}
	if err := seat("codex", []string{RoleQA}, Browser{Name: "kept while off"}); err != nil {
		t.Fatalf("a browser that is off is no one's concern: %v", err)
	}

	m, err := s.SaveMember(testContext, "", MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "claude", Browser: Browser{On: true, Name: " Chrome "}})
	if err != nil || !m.Browser.On || m.Browser.Name != "Chrome" {
		t.Fatalf("member %+v %v", m, err)
	}
	if _, err := s.SaveMember(testContext, m.ID, MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "codex", Browser: m.Browser}); err == nil {
		t.Fatal("moving a member to Codex left the browser on")
	}
	if _, err := s.SaveMember(testContext, "", MemberInput{Name: "Rex", Kinds: []string{RoleQA}, Engine: "codex", Browser: Browser{On: true}}); err == nil {
		t.Fatal("a Codex member was given the browser")
	}
	if _, err := s.SaveMember(testContext, "", MemberInput{Name: "Ida", Kinds: []string{RoleImplementer}, Engine: "claude", Browser: Browser{On: true}}); err == nil {
		t.Fatal("a member who isn't QA was given the browser")
	}
	if m, err = s.SaveMember(testContext, m.ID, MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "codex"}); err != nil || m.Browser.On {
		t.Fatalf("switching the browser off and moving to Codex: %+v %v", m, err)
	}
}

// Losing the QA role takes the browser with it, so the seat stays valid.
func TestASeatThatStopsBeingQALosesTheBrowser(t *testing.T) {
	p := Playbook{Template: "code", Roles: []Role{{Name: "Quinn", Kinds: []string{RoleResearcher, RoleQA}, Engine: "claude", Browser: Browser{On: true}}}}
	p.Rekind(0, []string{RoleResearcher})
	if p.Roles[0].Browser.On {
		t.Fatal("a researcher kept QA's browser")
	}
}

// QA's screenshots are kept as the task's attachments, through the same
// checks and limits as any, each saying which check it is evidence for.
func TestQAScreenshotsAreKeptWithTheirVerdict(t *testing.T) {
	s, _ := fixture(t)
	p := codeProject(t, s, "claude")
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A page"})
	verdict := NewVerdictID()
	shot := pngBytes(t)
	kept, err := s.AttachToVerdict(testContext, VerdictFiles{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Verdict: verdict, Files: []NewFile{{Name: "screenshot-1.png", Data: shot}}})
	if err != nil || len(kept) != 1 {
		t.Fatalf("kept %+v %v", kept, err)
	}
	if a := kept[0]; a.Verdict != verdict || a.Note != "" || a.Design != "" || a.Type != "image/png" || a.By != "QA" {
		t.Fatalf("attachment %+v", a)
	}
	if _, path, err := s.OpenAttachment(testContext, p.ID, task.ID, kept[0].ID); err != nil || path == "" {
		t.Fatalf("opening it: %v", err)
	}
	five := make([]NewFile, MaxScreenshots+1)
	for i := range five {
		five[i] = NewFile{Name: "s.png", Data: shot}
	}
	if _, err := s.AttachToVerdict(testContext, VerdictFiles{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Verdict: verdict, Files: five}); err == nil {
		t.Fatal("more screenshots than a check keeps were kept")
	}
	if _, err := s.AttachToVerdict(testContext, VerdictFiles{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Verdict: verdict, Files: []NewFile{{Name: "s.png", Data: []byte("not a picture")}}}); err == nil {
		t.Fatal("something that isn't an image was kept as a screenshot")
	}
	if _, err := s.AttachToVerdict(testContext, VerdictFiles{Project: p.ID, Task: task.ID, By: "QA", Kind: RoleQA, Files: []NewFile{{Name: "s.png", Data: shot}}}); err == nil {
		t.Fatal("a screenshot was kept for no check")
	}
	if names := keptFiles(t, s, task.ID); len(names) != 1 {
		t.Fatalf("files kept %v", names)
	}
}
