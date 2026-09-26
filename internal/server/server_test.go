package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
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
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	s := core.NewService(store, cfg)
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, _ := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	h := New(a, auth)
	return a, func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		r.Header.Set("X-Requested-With", "crew-assistant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
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
	model := c["model"].(map[string]any)
	model["claude_home"] = "/synthetic/claude"
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

// While the daemon finishes its work before stopping, work that would start
// a model is refused as unavailable, in words the owner can act on.
func TestAStoppingDaemonRefusesNewModelWork(t *testing.T) {
	a, call := ownerApp(t)
	stopped, stopTaking := context.WithCancel(context.Background())
	stopTaking()
	if err := a.Run(lifecycle.Stop{Graceful: stopped, Force: context.Background()}, true); err != nil {
		t.Fatal(err)
	}
	w := call("POST", "/api/setup/interview", `{"message":"Hello"}`)
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
