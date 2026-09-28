package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// A member's panel reads what its seat did on a task, and only the owner
// can: the state everyone polls leaves it out.
func TestTheOwnerReadsASeatsStepsOnATask(t *testing.T) {
	a, call := ownerApp(t)
	ctx := context.Background()
	p, err := a.Core.CreateProject(ctx, core.ProjectInput{Title: "Service", Brief: core.BriefInput{Goal: "Faster"}, Template: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.Core.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Cache the lookups"})
	if err != nil {
		t.Fatal(err)
	}
	step := core.TurnStep{TaskID: task.ID, Seat: "Ada Lovelace", Turn: "run", Item: "call", Kind: core.StepTool, Tool: "Bash", Input: `{"command":"cat secrets.env"}`, Output: "private output", Status: "completed"}
	if err := a.Core.RecordTurnStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + p.ID + "/tasks/" + task.ID + "/seats/Ada%20Lovelace/steps"
	w := call("GET", path, "")
	var got struct{ Steps []core.TurnStep }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Steps) != 1 || got.Steps[0].Output != "private output" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/projects/"+p.ID+"/tasks/"+task.ID+"/seats/Rune/steps", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"steps":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/projects/other/tasks/"+task.ID+"/seats/Ada/steps", ""); w.Code != 404 {
		t.Fatal("another project's task answered", w.Code)
	}
	if w := call("GET", "/api/state", ""); strings.Contains(w.Body.String(), "private output") {
		t.Fatal("the polled state carries a tool's output")
	}
	auth, _ := NewAuth(t.TempDir(), "http://127.0.0.1:8340", "", nil)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8340"+path, nil)
	r.RemoteAddr = "127.0.0.1:4321"
	anonymous := httptest.NewRecorder()
	New(a, auth).ServeHTTP(anonymous, r)
	if anonymous.Code != 401 || strings.Contains(anonymous.Body.String(), "private output") {
		t.Fatal("a caller who isn't signed in read a step", anonymous.Code)
	}
}
