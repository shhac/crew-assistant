package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// The owner sets how QA runs the app, and whether QA uses the browser, in
// project settings; the dashboard is told which engines have a browser.
func TestTheOwnerSetsHowQARunsTheApp(t *testing.T) {
	a, call := ownerApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Shop", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "Sell"}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", "/api/projects/"+p.ID+"/team", `{"template":"code","check":"make check"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	run := "/api/projects/" + p.ID + "/run"
	w := call("PUT", run, `{"run":{"setup":"npm run build","start":"npm start","url":"http://127.0.0.1:{port}/","ready":""}}`)
	var project core.Project
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &project) != nil || project.Playbook.Run == nil || project.Playbook.Run.Setup != "npm run build" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", run, `{"run":{"start":"npm start","url":"http://192.168.1.2:{port}/"}}`); w.Code != 400 || !strings.Contains(w.Body.String(), "this machine") {
		t.Fatal("a recipe off this machine was accepted", w.Code, w.Body.String())
	}
	if w := call("PUT", run, `{"run":null}`); w.Code != 200 || strings.Contains(w.Body.String(), `"run":{`) {
		t.Fatal("the recipe was not taken away", w.Code, w.Body.String())
	}
	browser := "/api/projects/" + p.ID + "/team/qa/browser"
	if w := call("PUT", browser, `{"on":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"browser":{"on":true}`) {
		t.Fatal("Codex QA, whose sandboxed sessions admit the browser, wasn't given it", w.Code, w.Body.String())
	}
	quinn, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Quinn", Kinds: []string{core.RoleQA}, Engine: "claude"})
	if w := call("PUT", "/api/projects/"+p.ID+"/team/qa", `{"member":"`+quinn.ID+`"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", browser, `{"on":true,"name":"Work"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"browser":{"on":true,"name":"Work"}`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", "/api/members/"+quinn.ID, `{"name":"Quinn","kinds":["qa"],"engine":"codex","browser":{"on":true}}`); w.Code != 200 {
		t.Fatal("a Codex member wasn't given the browser", w.Code, w.Body.String())
	}
	var defaults struct {
		Choices []struct {
			Engine  string `json:"engine"`
			Browser bool   `json:"browser"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(call("GET", "/api/config/defaults", "").Body.Bytes(), &defaults)
	offered := map[string]bool{}
	for _, c := range defaults.Choices {
		offered[c.Engine] = c.Browser
	}
	if !offered["claude"] || !offered["codex"] {
		t.Fatalf("browser choices %v", offered)
	}
}
