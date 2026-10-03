package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestReleaseSettingsEndpoint(t *testing.T) {
	for _, tool := range engine.Tools(true) {
		if strings.Contains(tool.Function.Name, "release") {
			t.Fatal("assistant release settings tool", tool.Function.Name)
		}
	}
	a, call := ownerApp(t)
	p, err := a.Core.CreateProject(context.Background(), core.ProjectInput{Title: "Service", Directories: []string{t.TempDir()}, Brief: core.BriefInput{Goal: "Ship"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + p.ID
	if w := call("PUT", path+"/team", `{"template":"code","check":"make check"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("PUT", path+"/landing", `{"via":"push","target":"main"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("PUT", path+"/release", `{"when":" after features ","check":" make release-check VERSION={version} ","github":"owner/repo","approve":"pm"}`)
	var project core.Project
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &project) != nil || project.Playbook.Release == nil || project.Playbook.Release.When != "after features" {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"when":"yes","approve":"none"}`, `{"when":"yes","github":"bad"}`, `{"when":"yes","check":"one\ntwo"}`} {
		if w := call("PUT", path+"/release", body); w.Code != 400 {
			t.Fatal("accepted", body, w.Code, w.Body.String())
		}
	}
	if w := call("PUT", path+"/release", `null`); w.Code != 200 || strings.Contains(w.Body.String(), `"release":{`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProjectCreationRejectsReleaseStateAndHistory(t *testing.T) {
	a, call := ownerApp(t)
	for _, field := range []string{"release", "releases"} {
		body := fmt.Sprintf(`{"title":"Service","directories":[%q],"brief":{"goal":"Ship"},%q:null}`, t.TempDir(), field)
		w := call("POST", "/api/projects", body)
		if w.Code != 400 {
			t.Fatal("creation accepted release input", w.Code, w.Body.String())
		}
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Projects) != 0 {
		t.Fatal("rejected creation wrote projects", snap.Projects)
	}
}
