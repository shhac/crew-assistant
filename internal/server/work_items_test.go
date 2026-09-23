package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestWorkItemRoutesPreserveDirectionAndRejectUnreviewedAcceptance(t *testing.T) {
	cfg := config.Default()
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, cfg)
	p, err := service.CreateProject(context.Background(), core.ProjectInput{Title: "Ongoing area"})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(service, cfg, filepath.Join(t.TempDir(), "config.json"), false)
	mux := http.NewServeMux()
	registerWorkItems(mux, a)
	request := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}
	w := request("/api/projects/"+p.ID+"/work-items", `{"title":"Export","objective":"Support CSV","acceptance_criteria":"CSV validates"}`)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var item core.WorkItem
	if err = json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	route := "/api/work-items/" + item.ID
	for i := 0; i < 2; i++ {
		w = request(route+"/steering", `{"message_id":"direction-1","message":"Preserve Unicode"}`)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	snapshot, _ := service.Snapshot(context.Background())
	if len(snapshot.Steering) != 1 {
		t.Fatal("retry duplicated steering")
	}
	w = request(route+"/accept", `{"review_revision":"stale","evidence":["Looks done"]}`)
	if w.Code < 400 {
		t.Fatal("accepted without attempts and current evidence")
	}
	w = request(route+"/accept", `{"review_revision":"stale","evidence":["Looks done"],"reviewer":"assistant"}`)
	if w.Code != 400 {
		t.Fatal("client spoofed reviewer", w.Code)
	}
}
