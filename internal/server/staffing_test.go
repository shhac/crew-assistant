package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func decoded[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if w.Code >= 300 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The project-level controls keep working, now writing the repository and
// the team, and the repository and team controls reach every project that
// shares them.
func TestRepositoriesAndTeamsThroughTheAPI(t *testing.T) {
	_, call := ownerServer(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	folders, _ := json.Marshal([]string{dir})
	newCode := func(title string) core.Project {
		p := decoded[core.Project](t, call("POST", "/api/projects", `{"title":"`+title+`","brief":{"goal":"Ship it","criteria":["Works"]},"directories":`+string(folders)+`}`))
		return decoded[core.Project](t, call("PUT", "/api/projects/"+p.ID+"/team", `{"template":"code","repo":`+string(folders[1:len(folders)-1])+`,"check":"make check"}`))
	}
	web := newCode("Web")
	if web.Playbook == nil || web.Playbook.Repo != dir || len(web.Scope.Repositories) != 1 {
		t.Fatalf("a code team chosen for a project registers its repository: %+v", web)
	}
	repoID := web.Scope.Repositories[0].ID
	// The project's own seats become a team, which a second project shares.
	team := decoded[core.Team](t, call("POST", "/api/teams", `{"name":"Core","from_project":"`+web.ID+`"}`))
	api := newCode("API")
	api = decoded[core.Project](t, call("PUT", "/api/projects/"+api.ID+"/setup", `{"team":"`+team.ID+`","repository":"`+repoID+`"}`))
	if api.Team != team.ID || api.Scope.Repositories[0].ID != repoID {
		t.Fatalf("setup %+v", api)
	}
	// The check, set from one project, is the shared repository's.
	decoded[core.Project](t, call("PUT", "/api/projects/"+web.ID+"/team/check", `{"check":"make test"}`))
	// A team's seat set by the team reaches both projects.
	decoded[core.Team](t, call("POST", "/api/teams/"+team.ID+"/seats", `{"seat":"Implementer"}`))
	// API leaves research out, for itself only.
	api = decoded[core.Project](t, call("PUT", "/api/projects/"+api.ID+"/seat-overrides", `{"overrides":[{"action":"exclude","kind":"researcher"}]}`))
	snap := decoded[core.Snapshot](t, call("GET", "/api/state", ""))
	byID := map[string]core.Project{}
	for _, p := range snap.Projects {
		byID[p.ID] = p
	}
	names := func(p core.Project) string {
		var out []string
		for _, r := range p.Playbook.Roles {
			out = append(out, r.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(byID[web.ID]); got != "Researcher,Implementer,Implementer #2,Reviewer,QA" {
		t.Fatalf("web seats %s", got)
	}
	if got := names(byID[api.ID]); got != "Implementer,Implementer #2,Reviewer,QA" {
		t.Fatalf("api seats %s", got)
	}
	if byID[api.ID].Playbook.Check != "make test" || len(snap.Repositories) != 2 || len(snap.Teams) != 1 {
		t.Fatalf("check %q, %d repositories, %d teams", byID[api.ID].Playbook.Check, len(snap.Repositories), len(snap.Teams))
	}
	if w := call("DELETE", "/api/teams/"+team.ID, ""); w.Code != 409 {
		t.Fatalf("deleting a team in use: %d %s", w.Code, w.Body.String())
	}
	// API's own repository, registered when its team was chosen, is now
	// unused and can go.
	for _, r := range snap.Repositories {
		if r.ID != repoID {
			if w := call("DELETE", "/api/repositories/"+r.ID, ""); w.Code != 200 {
				t.Fatalf("deleting an unused repository: %d %s", w.Code, w.Body.String())
			}
		}
	}
	if w := call("PUT", "/api/repositories/"+repoID, `{"name":"Mono","path":`+string(folders[1:len(folders)-1])+`,"check":"make check","areas":[{"name":"web","paths":["web/**"]}]}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call("PUT", "/api/projects/"+web.ID+"/setup", `{"team":"`+team.ID+`","repository":"`+repoID+`","areas":["web"]}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
