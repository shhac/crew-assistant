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
	if _, err := s.AnswerDecision(testContext, d.ID, ChoiceUseRecipe, FromOwner); err != nil {
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
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceNotNow, FromOwner); err != nil {
		t.Fatal(err)
	}
	d, _ = s.ProposeRunRecipe(testContext, p.ID, "Pim", recipe, "")
	if _, err := s.ChooseDecision(testContext, d.ID, ChoiceUseRecipe, FromOwner); err != nil {
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

// A member may allow the browser whatever their roles, on any engine whose
// sandboxed sessions admit it, as Claude's and Codex's both do. A project
// seat's own setting is QA's, for using the app.
func TestTheBrowserIsAMembersChoiceAndASeatsIsQAs(t *testing.T) {
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
	if err := seat("codex", []string{RoleQA}, Browser{On: true}); err != nil {
		t.Fatalf("Codex QA with the browser: %v", err)
	}
	if err := seat("claude", []string{RoleResearcher}, Browser{On: true}); err == nil {
		t.Fatal("a seat that isn't QA was given QA's browser setting")
	}
	if err := seat("codex", []string{RoleQA}, Browser{Name: "kept while off"}); err != nil {
		t.Fatalf("a browser that is off is no one's concern: %v", err)
	}

	m, err := s.SaveMember(testContext, "", MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "claude", Browser: Browser{On: true, Name: " Chrome "}})
	if err != nil || !m.Browser.On || m.Browser.Name != "Chrome" {
		t.Fatalf("member %+v %v", m, err)
	}
	if m, err = s.SaveMember(testContext, m.ID, MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "codex", Browser: m.Browser}); err != nil || !m.Browser.On {
		t.Fatalf("moving a member to Codex: %+v %v", m, err)
	}
	for _, kinds := range [][]string{{RoleImplementer}, {RoleDesigner}, {RoleReviewer}, {RolePM}} {
		if _, err := s.SaveMember(testContext, "", MemberInput{Name: "Ida " + kinds[0], Kinds: kinds, Engine: "codex", Browser: Browser{On: true}}); err != nil {
			t.Fatalf("a %s allowed the browser: %v", kinds[0], err)
		}
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
	shot := pngBytes(t)
	check := func(shots Screenshots) (Task, Verdict) {
		t.Helper()
		var recorded Verdict
		got, err := s.UpdateTaskWithVerdict(testContext, task.ID, Verdict{Role: "QA", Outcome: VerdictPass}, shots, func(t *Task, _ *Project, v Verdict) (string, error) {
			t.Verdicts = append(t.Verdicts, v)
			recorded = v
			return "", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got, recorded
	}
	got, v := check(Screenshots{By: "QA", Files: []NewFile{{Name: "screenshot-1.png", Data: shot}}, Omitted: 2})
	if v.ID == "" || len(v.Evidence) != 2 || len(got.Attachments) != 1 || v.Evidence[0].Attachment != got.Attachments[0].ID || !strings.HasPrefix(v.Evidence[1].Text, "2 more screenshots were taken and not kept") {
		t.Fatalf("verdict %+v, attachments %+v", v, got.Attachments)
	}
	kept := got.Attachments[0]
	if kept.Verdict != v.ID || kept.Note != "" || kept.Design != "" || kept.Type != "image/png" || kept.By != "QA" || kept.Kind != RoleQA {
		t.Fatalf("attachment %+v", kept)
	}
	if _, path, err := s.OpenAttachment(testContext, p.ID, task.ID, kept.ID); err != nil || path == "" {
		t.Fatalf("opening it: %v", err)
	}
	// What can't be kept is said in the verdict, which is still recorded,
	// with no id for screenshots it doesn't have.
	five := make([]NewFile, MaxScreenshots+1)
	for i := range five {
		five[i] = NewFile{Name: "s.png", Data: shot}
	}
	if got, v = check(Screenshots{By: "QA", Files: five}); v.ID != "" || len(got.Verdicts) != 2 || len(v.Evidence) != 1 || !strings.HasPrefix(v.Evidence[0].Text, "5 screenshots could not be kept: a check can keep at most") {
		t.Fatalf("more screenshots than a check keeps: %+v", v)
	}
	if _, v = check(Screenshots{By: "QA", Files: []NewFile{{Name: "s.png", Data: []byte("not a picture")}}}); v.ID != "" || len(v.Evidence) != 1 || !strings.Contains(v.Evidence[0].Text, "it is not a PNG image") {
		t.Fatalf("something that isn't an image: %+v", v)
	}
	if got, _ = check(Screenshots{}); len(got.Attachments) != 1 {
		t.Fatalf("attachments %+v", got.Attachments)
	}
	if names := keptFiles(t, s, task.ID); len(names) != 1 {
		t.Fatalf("files kept %v", names)
	}
}

// Screenshots are kept only with a verdict the same update records: when
// it leaves the verdict out, or fails, no attachment is recorded and the
// files written for them are removed.
func TestQAScreenshotsAreNotKeptWithoutTheirVerdict(t *testing.T) {
	s, _ := fixture(t)
	p := codeProject(t, s, "claude")
	task, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A page"})
	shots := Screenshots{By: "QA", Files: []NewFile{{Name: "screenshot-1.png", Data: pngBytes(t)}, {Name: "screenshot-2.png", Data: pngBytes(t)}}}
	var given Verdict
	left := func(t *Task, _ *Project, v Verdict) (string, error) {
		given = v
		return "", nil
	}
	failing := func(t *Task, _ *Project, v Verdict) (string, error) {
		t.Verdicts = append(t.Verdicts, v)
		return "", errors.New("the check can't be recorded")
	}
	if _, err := s.UpdateTaskWithVerdict(testContext, task.ID, Verdict{Role: "QA"}, shots, left); err != nil || given.ID == "" {
		t.Fatalf("the verdict was not offered its screenshots: %+v %v", given, err)
	}
	if _, err := s.UpdateTaskWithVerdict(testContext, task.ID, Verdict{Role: "QA"}, shots, failing); err == nil {
		t.Fatal("a failed update was not reported")
	}
	// A turn whose claim has gone records nothing, its screenshots included.
	if _, err := s.UpdateTaskWithVerdict(Fenced(testContext, task.ID, "gone"), task.ID, Verdict{Role: "QA"}, shots, failing); !errors.Is(err, ErrStale) {
		t.Fatalf("a stale turn: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	got, _ := snap.FindTask(task.ID)
	if len(got.Attachments) != 0 || len(got.Verdicts) != 0 {
		t.Fatalf("kept without their verdict: %+v, verdicts %+v", got.Attachments, got.Verdicts)
	}
	if names := keptFiles(t, s, task.ID); len(names) != 0 {
		t.Fatalf("files left %v", names)
	}
}
