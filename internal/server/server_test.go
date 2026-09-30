package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

// ownerServer is a dashboard over a fresh store, called as the owner.
func ownerServer(t *testing.T) (*core.Service, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	a, call := ownerApp(t)
	return a.Core, call
}

func ownerApp(t *testing.T) (*app.App, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	a, auth, h := newDashboard(t, config.Default())
	return a, func(method, path, body string) *httptest.ResponseRecorder {
		return send(h, auth, method, path, strings.NewReader(body), asOwner)
	}
}

func newDashboard(t *testing.T, cfg config.Config) (*app.App, *Auth, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, auth, New(a, auth)
}

// caller is who a request comes from: owner carries the admin token, and csrf
// the header only the dashboard's own pages send.
type caller struct {
	owner, csrf bool
	contentType string
}

var asOwner = caller{owner: true, csrf: true}

func send(h http.Handler, auth *Auth, method, path string, body io.Reader, as caller) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, body)
	r.RemoteAddr = "127.0.0.1:4321"
	if as.owner {
		r.Header.Set("Authorization", "Bearer "+auth.admin)
	}
	if as.csrf {
		r.Header.Set("X-Requested-With", "crew-assistant")
	}
	if as.contentType != "" {
		r.Header.Set("Content-Type", as.contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDashboardProjectDecisionMemoryFlow(t *testing.T) {
	s, call := ownerServer(t)
	created := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var project core.Project
	_ = json.Unmarshal(created.Body.Bytes(), &project)
	d, err := s.CreateDecision(context.Background(), core.DecisionInput{ProjectID: project.ID, Title: "CSV first?", Context: "Excel adds effort", Recommendation: "CSV first", Choices: []string{"CSV", "Excel"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/decisions/"+d.ID+"/resolve", `{"choice":"CSV"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("POST", "/api/decisions/"+d.ID+"/resolve", `{"choice":"Excel"}`); w.Code != 409 {
		t.Fatal("repeated decision overwrote answer")
	}

	custom, _ := s.CreateDecision(context.Background(), core.DecisionInput{Title: "Custom?", Context: "Options incomplete", Recommendation: "One", Choices: []string{"One", "Two"}})
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"choice":"One","answer":"Other"}`); w.Code != 400 {
		t.Fatal("ambiguous answer accepted", w.Code)
	}
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"choice":"Three"}`); w.Code != 409 {
		t.Fatal("a choice the decision does not offer was accepted", w.Code)
	}
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"answer":"One"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"disposition":"custom"`) {
		t.Fatal("typed words that spell a choice must stay the owner's words", w.Code, w.Body.String())
	}
	custom, _ = s.CreateDecision(context.Background(), core.DecisionInput{Title: "Custom?", Context: "Options incomplete", Recommendation: "One", Choices: []string{"One", "Two"}})
	if w := call("POST", "/api/decisions/"+custom.ID+"/resolve", `{"answer":"Use the existing option"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"disposition":"custom"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	stale, _ := s.CreateDecision(context.Background(), core.DecisionInput{Title: "Stale?", Context: "Already resolved elsewhere", Recommendation: "One", Choices: []string{"One", "Two"}})
	if w := call("POST", "/api/decisions/"+stale.ID+"/dismiss", `{"reason":""}`); w.Code != 400 {
		t.Fatal("blank reason accepted")
	}
	if w := call("POST", "/api/decisions/"+stale.ID+"/dismiss", `{"reason":"Already configured"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"dismissed"`) || strings.Contains(w.Body.String(), `"answer"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	memory := call("POST", "/api/memories", `{"content":"Prefer concise updates"}`)
	if memory.Code != 201 {
		t.Fatal(memory.Body.String())
	}
	var m core.Memory
	_ = json.Unmarshal(memory.Body.Bytes(), &m)
	if w := call("DELETE", "/api/memories/"+m.ID, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("GET", "/api/state", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "resolved") {
		t.Fatal(w.Body.String())
	}
	if w := call("GET", "/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "<html") {
		t.Fatal("embedded UI missing")
	}
}

// The dashboard receives execution health alongside the state it is already
// showing, so a stopped worker is visible without opening each project, and a
// quiet decision queue never stands in for a healthy one.
func TestCorrectMemoryReportsRefusalsToTheOwner(t *testing.T) {
	_, call := ownerServer(t)

	created := call("POST", "/api/memories", `{"content":"Worker model information is unavailable.","kind":"observation"}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	var memory core.Memory
	_ = json.Unmarshal(created.Body.Bytes(), &memory)
	if memory.Source != "owner" {
		t.Fatalf("source = %q, want owner for a memory the owner recorded", memory.Source)
	}

	corrected := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"The project worker runs Opus 5."}`)
	if corrected.Code != 201 {
		t.Fatal(corrected.Body.String())
	}

	again := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"Third attempt."}`)
	if again.Code != 409 {
		t.Fatalf("correcting an already-corrected memory returned %d, want 409", again.Code)
	}
	missing := call("POST", "/api/memories/does-not-exist/correct", `{"content":"Anything."}`)
	if missing.Code != 404 {
		t.Fatalf("correcting an unknown memory returned %d, want 404", missing.Code)
	}
	empty := call("POST", "/api/memories/"+memory.ID+"/correct", `{"content":"  "}`)
	if empty.Code != 400 {
		t.Fatalf("an empty correction returned %d, want 400", empty.Code)
	}

	var state struct {
		Memories []core.Memory `json:"memories"`
	}
	_ = json.Unmarshal(call("GET", "/api/state", "").Body.Bytes(), &state)
	if len(state.Memories) != 2 {
		t.Fatalf("expected the original and its replacement, got %d", len(state.Memories))
	}
}

// The dashboard downloads an artifact by token. The route must stay behind
// owner access, must not accept a path, and must not let the name segment
// steer resolution.

func TestTheOwnerOrdersTheToDoListAndMessagesTheTeam(t *testing.T) {
	s, call := ownerServer(t)
	p, err := s.CreateProject(context.Background(), core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "First"})
	second, _ := s.QueueTask(context.Background(), p.ID, core.TaskInput{Objective: "Second"})
	order := call("PUT", "/api/projects/"+p.ID+"/tasks/order", `{"task_ids":["`+second.ID+`","`+first.ID+`"]}`)
	if order.Code != 200 || strings.Index(order.Body.String(), "Second") > strings.Index(order.Body.String(), "First") {
		t.Fatal(order.Code, order.Body.String())
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/tasks/order", `{"task_ids":["`+first.ID+`"]}`); w.Code != 409 {
		t.Fatal("an incomplete order was accepted", w.Code)
	}
	sent := call("POST", "/api/projects/"+p.ID+"/tasks/"+second.ID+"/messages", `{"to":"Writer","text":"Keep it to one page"}`)
	if sent.Code != 201 || !strings.Contains(sent.Body.String(), `"from":"owner"`) {
		t.Fatal(sent.Code, sent.Body.String())
	}
	state := call("GET", "/api/state", "")
	if !strings.Contains(state.Body.String(), `"stage":"todo"`) || !strings.Contains(state.Body.String(), "Keep it to one page") {
		t.Fatal("the state does not carry stages and messages")
	}
}

func TestTheOwnerFillsARoleAndSetsWhereACodeTeamWorks(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	ada, _ := s.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	filled := call("PUT", "/api/projects/"+p.ID+"/team/implementer", `{"member":"`+ada.ID+`"}`)
	if filled.Code != 200 || !strings.Contains(filled.Body.String(), `"member":"`+ada.ID+`"`) {
		t.Fatal(filled.Code, filled.Body.String())
	}
	emptied := call("PUT", "/api/projects/"+p.ID+"/team/implementer", `{"member":""}`)
	if emptied.Code != 200 || strings.Contains(emptied.Body.String(), ada.ID) {
		t.Fatal(emptied.Code, emptied.Body.String())
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/workspace", `{"repo":"","branch_prefix":"crew/","prepare":[],"sign":""}`); w.Code == 200 {
		t.Fatal("a writing team was given a workspace")
	}
}

// The owner adds and removes seats filled like another, and sets how many
// tasks may be under way at once, which the project then shows.
func TestTheOwnerAddsSeatsAndSetsHowMuchRunsAtOnce(t *testing.T) {
	s, call := ownerServer(t)
	p, err := s.CreateProject(context.Background(), core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	added := call("POST", "/api/projects/"+p.ID+"/team/seats", `{"seat":"Writer"}`)
	if added.Code != 200 || !strings.Contains(added.Body.String(), `"name":"Writer #2"`) {
		t.Fatal(added.Code, added.Body.String())
	}
	if missing := call("POST", "/api/projects/"+p.ID+"/team/seats", `{"seat":"Nobody"}`); missing.Code != 404 {
		t.Fatal(missing.Code, missing.Body.String())
	}
	set := call("PUT", "/api/projects/"+p.ID+"/parallel", `{"max_active":3}`)
	if set.Code != 200 || !strings.Contains(set.Body.String(), `"max_active":3`) {
		t.Fatal(set.Code, set.Body.String())
	}
	if bad := call("PUT", "/api/projects/"+p.ID+"/parallel", `{"max_active":-1}`); bad.Code == 200 {
		t.Fatal("a negative cap was taken")
	}
	limits := call("PUT", "/api/projects/"+p.ID+"/stage-limits", `{"stage_limits":{"implementing":2,"reviewing":0}}`)
	if limits.Code != 200 || !strings.Contains(limits.Body.String(), `"stage_limits":{"implementing":2}`) {
		t.Fatal(limits.Code, limits.Body.String())
	}
	for _, body := range []string{`{"stage_limits":{"todo":1}}`, `{"stage_limits":{"qa":-1}}`, `{"stage_limits":{"testing":1}}`} {
		if bad := call("PUT", "/api/projects/"+p.ID+"/stage-limits", body); bad.Code != 400 {
			t.Fatal(body, bad.Code, bad.Body.String())
		}
	}
	removed := call("DELETE", "/api/projects/"+p.ID+"/team/seats/Writer%20%232", "")
	if removed.Code != 200 || strings.Contains(removed.Body.String(), "Writer #2") {
		t.Fatal(removed.Code, removed.Body.String())
	}
	if last := call("DELETE", "/api/projects/"+p.ID+"/team/seats/Writer", ""); last.Code == 200 {
		t.Fatal("the team's only writer was removed")
	}
}

// The owner adds someone to a role and takes them out of it again; a seat
// that holds another role keeps it.
func TestTheOwnerAddsAndRemovesPeopleByRole(t *testing.T) {
	s, call := ownerServer(t)
	p, err := s.CreateProject(context.Background(), core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	added := call("POST", "/api/projects/"+p.ID+"/team/seats", `{"kind":"reviewer","member":""}`)
	if added.Code != 200 || !strings.Contains(added.Body.String(), `"name":"Reviewer #2"`) {
		t.Fatal(added.Code, added.Body.String())
	}
	if pm := call("POST", "/api/projects/"+p.ID+"/team/seats", `{"kind":"pm","member":""}`); pm.Code == 200 {
		t.Fatal("no template fills the PM role, so someone must be chosen")
	}
	removed := call("DELETE", "/api/projects/"+p.ID+"/team/seats/Reviewer%20%232?kind=reviewer", "")
	if removed.Code != 200 || strings.Contains(removed.Body.String(), "Reviewer #2") {
		t.Fatal(removed.Code, removed.Body.String())
	}
	if wrong := call("DELETE", "/api/projects/"+p.ID+"/team/seats/Writer?kind=reviewer", ""); wrong.Code == 200 {
		t.Fatal("the writer was taken out of a role it doesn't hold")
	}
	if last := call("DELETE", "/api/projects/"+p.ID+"/team/seats/Reviewer?kind=reviewer", ""); last.Code == 200 {
		t.Fatal("the team's only reviewer was removed")
	}
}

func TestErrorsReachTheOwnerWithoutTheirInternalLabels(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("this request has already finished: %w", core.ErrConflict): "This request has already finished",
		core.ErrNotFound: "Not found",
		fmt.Errorf("the demo doesn't run models: %w", core.ErrChatValidation): "The demo doesn't run models",
	} {
		if got := ownerText(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}

// A dashboard left open across the upgrade to engines still saves: its model
// section's CLI settings and its used-percent limit land where they now live.
func TestAConfigInTheEarlierLayoutStillSaves(t *testing.T) {
	a, call := ownerApp(t)
	var c map[string]any
	w := call("GET", "/api/config", "")
	if json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatal(w.Body.String())
	}
	c["model"] = map[string]any{"claude_home": "/synthetic/claude"}
	delete(c, "engines")
	c["limits"].(map[string]any)["role_usage"] = map[string]any{"claude_max_used_percent": 95, "on_unavailable": "pause"}
	body, _ := json.Marshal(c)
	if w := call("PUT", "/api/config", string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	claude := a.Config().Engines.Claude
	if claude.Home != "/synthetic/claude" || *claude.UsageFloor.WeekPercent != 5 || claude.OnUnknownUsage != "pause" {
		t.Fatalf("old layout lost: %+v", claude)
	}
	if w := call("GET", "/api/config/defaults", ""); !strings.Contains(w.Body.String(), `"usage_floor":10`) || !strings.Contains(w.Body.String(), `"bin":"claude"`) {
		t.Fatal("defaults", w.Body.String())
	}
	before := a.Config()
	for _, body := range []string{
		`{"model":{"engine":"codex","typo":1}}`,
		`{"model":{"engine":"codex","codex_home":"/synthetic/codex","typo":1}}`,
		`{} {}`,
		`{"model":{"codex_home":"/synthetic/codex"}} {}`,
	} {
		if w := call("PUT", "/api/config", body); w.Code != 400 {
			t.Errorf("%s was accepted: %d", body, w.Code)
		}
	}
	if !reflect.DeepEqual(a.Config(), before) {
		t.Fatal("a refused config changed the saved one")
	}
}

// Who may reach the dashboard over the tailnet is set where the daemon runs,
// never by a browser that has been paired with it.
func TestAPairedBrowserCannotChangeWhoIsAllowedIn(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Dashboard.AllowedUsers = []string{"owner@example.test"}
	configPath := filepath.Join(dir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := app.New(core.NewService(store, cfg), cfg, configPath, app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	send := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Requested-With", "crew-assistant")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	code, err := Pair(dir)
	if err != nil {
		t.Fatal(err)
	}
	login := send("POST", "/api/session", `{"token":"`+code+`"}`, nil)
	cookies := login.Result().Cookies()
	if login.Code != 200 || len(cookies) != 1 {
		t.Fatal(login.Code, login.Body.String())
	}
	if w := send("GET", "/api/config", "", cookies[0]); w.Code != 200 {
		t.Fatal("the paired browser was not let in", w.Code)
	}
	changed := a.Config()
	changed.Dashboard.AllowedUsers = append(changed.Dashboard.AllowedUsers, "intruder@example.test")
	body, _ := json.Marshal(changed)
	if w := send("PUT", "/api/config", string(body), cookies[0]); w.Code != 400 || !strings.Contains(w.Body.String(), "stopping the daemon") {
		t.Fatal("a paired browser changed who is allowed in", w.Code, w.Body.String())
	}
	if got := a.Config().Dashboard.AllowedUsers; !reflect.DeepEqual(got, cfg.Dashboard.AllowedUsers) {
		t.Fatalf("allowed users became %v", got)
	}
	if now, err := os.ReadFile(configPath); err != nil || !bytes.Equal(now, saved) {
		t.Fatalf("the config file changed: %v", err)
	}
}

// While the daemon finishes its work before stopping, work that would start
// a model is refused as unavailable, in words the owner can act on.
func TestAStoppingDaemonRefusesNewModelWork(t *testing.T) {
	a, call := ownerApp(t)
	stopped, stopTaking := context.WithCancel(context.Background())
	stopTaking()
	if err := a.Run(lifecycle.Stop{Graceful: stopped, Force: context.Background()}, true); err != nil {
		t.Fatal(err)
	}
	w := call("POST", "/api/setup/member/interview", `{"message":"Hello"}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Crew-assistant is stopping") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/chat/suggestion", `{"after":"reply"}`); w.Code != 503 {
		t.Fatal("suggestion", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/state", ""); !strings.Contains(w.Body.String(), `"stopping":true`) {
		t.Fatal("the dashboard isn't told", w.Body.String())
	}
}

// The owner links two tasks of a project from the dashboard, and unlinks
// them; a task of another project is not theirs to link from here.
func TestTheOwnerLinksTasks(t *testing.T) {
	_, call := ownerServer(t)
	var project, other core.Project
	for _, p := range []*core.Project{&project, &other} {
		w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
		_ = json.Unmarshal(w.Body.Bytes(), p)
	}
	queue := func(projectID, objective string) core.Task {
		var task core.Task
		w := call("POST", "/api/projects/"+projectID+"/tasks", `{"objective":"`+objective+`","criteria":[]}`)
		_ = json.Unmarshal(w.Body.Bytes(), &task)
		return task
	}
	schema, api, elsewhere := queue(project.ID, "Schema"), queue(project.ID, "API"), queue(other.ID, "Elsewhere")
	w := call("POST", "/api/projects/"+project.ID+"/tasks/"+api.ID+"/links", `{"relation":"depends_on","task":"`+schema.ID+`"}`)
	var linked core.Task
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &linked) != nil || len(linked.DependsOn) != 1 || linked.LinkedBy["depends_on:"+schema.ID].By != core.LinkedByOwner {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/projects/"+other.ID+"/tasks/"+api.ID+"/links", `{"relation":"relates_to","task":"`+elsewhere.ID+`"}`); w.Code != 404 {
		t.Fatal("linked across projects", w.Code)
	}
	if w := call("DELETE", "/api/projects/"+project.ID+"/tasks/"+schema.ID+"/links/"+api.ID, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("DELETE", "/api/projects/"+project.ID+"/tasks/"+schema.ID+"/links/"+api.ID, ""); w.Code != 404 {
		t.Fatal("unlinked twice", w.Code)
	}
}

// What the owner asks for on the dashboard goes to the team's PM for triage
// first, and straight to the to-do list on a team without one.
func TestTheOwnersRequestGoesToTriageWhenTheTeamHasAPM(t *testing.T) {
	s, call := ownerServer(t)
	ask := func(pm bool) core.Task {
		var project core.Project
		w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
		_ = json.Unmarshal(w.Body.Bytes(), &project)
		if pm {
			playbook := *project.Playbook
			playbook.Roles = append(playbook.Roles, core.Role{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
			if _, err := s.SetPlaybook(context.Background(), project.ID, playbook); err != nil {
				t.Fatal(err)
			}
		}
		var task core.Task
		w = call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Schema","criteria":[]}`)
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &task) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		return task
	}
	if task := ask(true); task.Status != core.TaskTriage || task.Stage != core.StageTriage {
		t.Fatalf("with a PM the request is %s in %s", task.Status, task.Stage)
	}
	if task := ask(false); task.Status != core.TaskQueued {
		t.Fatalf("without a PM the request is %s", task.Status)
	}
}

// The owner leaves notes on a task and undoes a change the team made to its
// requirements.
func TestTheOwnerLeavesNotesAndUndoesATeamsEdit(t *testing.T) {
	a, call := ownerServer(t)
	var project core.Project
	w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &project)
	var task core.Task
	w = call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Schema","criteria":["Owner's words"]}`)
	_ = json.Unmarshal(w.Body.Bytes(), &task)
	var note core.Note
	if w := call("POST", "/api/projects/"+project.ID+"/tasks/"+task.ID+"/notes", `{"text":"Keep the old columns"}`); w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &note) != nil || note.By != core.FromOwner {
		t.Fatal(w.Code, w.Body.String())
	}
	edited, err := a.EditTask(context.Background(), core.EditInput{Project: project.ID, Task: task.ID, By: "Rhea", Kind: core.RoleResearcher, Criteria: []string{"Reworded"}})
	if err != nil {
		t.Fatal(err)
	}
	var undone core.Task
	w = call("POST", "/api/projects/"+project.ID+"/tasks/"+task.ID+"/edits/"+edited.Edits[0].ID+"/undo", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &undone) != nil || len(undone.Criteria) != 1 || undone.Criteria[0] != "Owner's words" || len(undone.Notes) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/projects/"+project.ID+"/tasks/"+task.ID+"/edits/nope/undo", ""); w.Code != 404 {
		t.Fatal("undid an edit that doesn't exist", w.Code)
	}
}

// The owner renames a project's task ID prefix, and the routes that name a
// task take its readable ID as well as its canonical one.
func TestTheOwnerRenamesAPrefixAndNamesTasksByReadableID(t *testing.T) {
	_, call := ownerServer(t)
	var project, other core.Project
	for _, p := range []*core.Project{&project, &other} {
		w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
		_ = json.Unmarshal(w.Body.Bytes(), p)
	}
	if project.Prefix != "EXP" || other.Prefix == "" || other.Prefix == project.Prefix {
		t.Fatalf("prefixes %q and %q", project.Prefix, other.Prefix)
	}
	var schema, api core.Task
	for _, task := range []*core.Task{&schema, &api} {
		w := call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Work","criteria":[]}`)
		_ = json.Unmarshal(w.Body.Bytes(), task)
	}
	if schema.Ref != "EXP-1" || api.Ref != "EXP-2" {
		t.Fatalf("readable IDs %q and %q", schema.Ref, api.Ref)
	}
	w := call("PUT", "/api/projects/"+project.ID+"/prefix", `{"prefix":"csv"}`)
	var renamed core.Project
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &renamed) != nil || renamed.Prefix != "CSV" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/projects/"+other.ID+"/prefix", `{"prefix":"Csv"}`); w.Code != 409 {
		t.Fatal("two projects share a prefix", w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/projects/"+other.ID+"/prefix", `{"prefix":"no way"}`); w.Code != 400 {
		t.Fatal("an invalid prefix was accepted", w.Code)
	}
	w = call("POST", "/api/projects/"+project.ID+"/tasks/csv-2/links", `{"relation":"depends_on","task":"CSV-1"}`)
	var linked core.Task
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &linked) != nil || linked.ID != api.ID || len(linked.DependsOn) != 1 || linked.DependsOn[0] != schema.ID || linked.Ref != "CSV-2" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/projects/"+project.ID+"/tasks/order", `{"task_ids":["CSV-2","`+schema.ID+`"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The owner renames a project, and only its title changes: its prefix,
// task IDs and links stay as they were.
func TestTheOwnerRenamesAProject(t *testing.T) {
	_, call := ownerServer(t)
	var project core.Project
	_ = json.Unmarshal(call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`).Body.Bytes(), &project)
	var schema, api core.Task
	for _, task := range []*core.Task{&schema, &api} {
		w := call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Work","criteria":[]}`)
		_ = json.Unmarshal(w.Body.Bytes(), task)
	}
	if w := call("POST", "/api/projects/"+project.ID+"/tasks/EXP-2/links", `{"relation":"depends_on","task":"EXP-1"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("PUT", "/api/projects/"+project.ID+"/title", `{"title":" Spreadsheet export "}`)
	var renamed core.Project
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &renamed) != nil || renamed.Title != "Spreadsheet export" || renamed.ID != project.ID || renamed.Prefix != "EXP" || renamed.NextTask != 3 {
		t.Fatal(w.Code, w.Body.String())
	}
	var state struct {
		Tasks []core.Task `json:"tasks"`
	}
	_ = json.Unmarshal(call("GET", "/api/state", "").Body.Bytes(), &state)
	for _, task := range state.Tasks {
		switch task.ID {
		case schema.ID:
			if task.Ref != "EXP-1" {
				t.Fatalf("schema %+v", task)
			}
		case api.ID:
			if task.Ref != "EXP-2" || len(task.DependsOn) != 1 || task.DependsOn[0] != schema.ID {
				t.Fatalf("api %+v", task)
			}
		}
	}
	for body, message := range map[string]string{
		`{"title":"  "}`: "A project needs a name",
		`{"title":"` + strings.Repeat("x", 201) + `"}`: "A project name can be at most 200 characters",
	} {
		if w := call("PUT", "/api/projects/"+project.ID+"/title", body); w.Code != 400 || !strings.Contains(w.Body.String(), message) {
			t.Fatal("a bad name was accepted", w.Code, w.Body.String())
		}
	}
	if w := call("PUT", "/api/projects/nope/title", `{"title":"Name"}`); w.Code != 404 {
		t.Fatal("renamed a project that doesn't exist", w.Code)
	}
}

// A task's place is readable from the dashboard's API, and only a code
// task's draft can be changed by hand.
func TestATasksPlaceAndDraftsByHand(t *testing.T) {
	_, call := ownerServer(t)
	var project core.Project
	w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &project)
	var task core.Task
	w = call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Write it","criteria":[]}`)
	_ = json.Unmarshal(w.Body.Bytes(), &task)
	w = call("GET", "/api/projects/"+project.ID+"/tasks/"+task.ID+"/place", "")
	var place map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &place) != nil || place["workspace"] == "" || place["running"] != false {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/projects/other/tasks/"+task.ID+"/place", ""); w.Code != 404 {
		t.Fatal("a task was found under another project", w.Code)
	}
	if w := call("POST", "/api/projects/"+project.ID+"/tasks/"+task.ID+"/drafts", `{"ref":"main","note":"","approve":false}`); w.Code != 400 || !strings.Contains(w.Body.String(), "code task") {
		t.Fatal(w.Code, w.Body.String())
	}
}
