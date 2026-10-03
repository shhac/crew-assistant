package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func TestDecisionResolveCarriesSplit(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.ProjectInput{Title: "Docs", Template: "draft", Brief: core.BriefInput{Goal: "Update"}})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(ctx, p.ID, core.TaskInput{Objective: "Update README", Criteria: []string{"README; CI; release"}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.OpenTaskDecision(ctx, task.ID, core.DecisionEscalation, core.DecisionInput{Title: "Split?", Context: "Blocked", Recommendation: "Split", Choices: []string{"Split it", "Keep it for the team"}, OwnerStep: &core.OwnerStep{Criterion: task.Criteria[0], Step: task.Criteria[0]}})
	if err != nil {
		t.Fatal(err)
	}
	url := "/api/decisions/" + d.ID + "/resolve"
	for _, body := range []string{
		`{"choice":"Split it","split":{"team":" CI ","owner":"CI"}}`,
		`{"choice":"Split it","split":{"team":" ","owner":"CI"}}`,
		`{"choice":"Keep it for the team","split":{"team":"README","owner":"CI"}}`,
		`{"answer":"Split it","split":{"team":"README","owner":"CI"}}`,
	} {
		if w := call("POST", url, body); w.Code != 400 {
			t.Fatalf("invalid split: %d %s", w.Code, w.Body.String())
		}
	}
	w := call("POST", url, `{"choice":"Split it","split":{"team":" README ","owner":" CI and release "}}`)
	if w.Code != 200 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	var got core.Decision
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != core.DecisionResolved || got.Split == nil || *got.Split != (core.OwnerSplit{Team: "README", Owner: "CI and release"}) {
		t.Fatalf("lost split: %+v", got)
	}
	if w := call("POST", url, `{"choice":"Split it","split":{"team":"README","owner":"CI"}}`); w.Code != 409 {
		t.Fatalf("duplicate resolve: %d", w.Code)
	}
}
