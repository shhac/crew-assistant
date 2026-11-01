package server

import (
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerPMPolicyRoutesFenceRevisionsAndUndo(t *testing.T) {
	service, call := ownerServer(t)
	p := decoded[core.Project](t, call("POST", "/api/projects", `{"title":"Synthetic","brief":{"goal":"Works"}}`))
	p = decoded[core.Project](t, call("PUT", "/api/projects/"+p.ID+"/team", `{"template":"draft"}`))
	path := "/api/projects/" + p.ID + "/pm-policies/close_duplicate"
	decoded[map[string]bool](t, call("PUT", path, `{"on":false,"revision":0}`))
	if got := call("PUT", path, `{"on":true,"revision":0}`); got.Code != 409 {
		t.Fatalf("%d %s", got.Code, got.Body.String())
	}
	decoded[map[string]bool](t, call("PUT", path, `{"on":null,"revision":1}`))
	team := decoded[core.Team](t, call("POST", "/api/teams", `{"name":"Team","template":"draft"}`))
	decoded[map[string]bool](t, call("PUT", "/api/teams/"+team.ID+"/pm-policies/rescope", `{"on":false,"revision":0}`))
	if got := call("PUT", "/api/teams/"+team.ID+"/pm-policies/unknown", `{"on":true,"revision":1}`); got.Code < 400 {
		t.Fatal("unknown policy accepted")
	}
	first, err := service.QueueTask(t.Context(), p.ID, core.TaskInput{Objective: "Same thing", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.QueueTask(t.Context(), p.ID, core.TaskInput{Objective: "Original", Criteria: []string{"Works"}})
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.CloseDuplicate(t.Context(), p.ID, first.ID, second.ID, "same outcome", "Manager", first)
	if err != nil {
		t.Fatal(err)
	}
	restored := decoded[core.Task](t, call("POST", "/api/projects/"+p.ID+"/tasks/"+first.ID+"/pm-actions/"+action.ID+"/undo", `{}`))
	if restored.Status != core.TaskQueued {
		t.Fatal(restored)
	}
	// The owner route reaches the checked undo rather than accepting arbitrary IDs.
	if got := call("POST", "/api/projects/"+p.ID+"/tasks/missing/pm-actions/missing/undo", `{}`); got.Code != 404 {
		t.Fatalf("%d %s", got.Code, got.Body.String())
	}
}
