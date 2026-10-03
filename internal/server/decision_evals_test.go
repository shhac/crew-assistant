package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerChecksTaskAPIAndDecisionEvaluationExport(t *testing.T) {
	s, call := ownerServer(t)
	p, err := s.CreateProject(context.Background(), core.ProjectInput{Title: "Notes", Template: "draft", Brief: core.BriefInput{Goal: "Notes"}})
	if err != nil {
		t.Fatal(err)
	}
	w := call("POST", "/api/projects/"+p.ID+"/tasks", `{"objective":"Build","criteria":["Tests"],"owner_checks":["Live check"]}`)
	var task core.Task
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &task) != nil || len(task.OwnerChecks) != 1 || task.OwnerChecks[0] != "Live check" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	d, err := s.CreateDecision(context.Background(), core.DecisionInput{Title: "Which?", Context: "Context", Recommendation: "Yes", Choices: []string{"Yes", "No"}})
	if err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/decisions/"+d.ID+"/resolve", `{"choice":"Yes"}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = call("GET", "/api/decisions/evaluations.jsonl", "")
	var e core.DecisionEvaluation
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.AnsweredBy != core.FromOwner {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w = call("GET", "/api/state", ""); strings.Contains(w.Body.String(), "decision_evaluations") {
		t.Fatal("evaluation table leaked into state")
	}
	_, auth, h := newDashboard(t, config.Default())
	if w = send(h, auth, http.MethodGet, "/api/decisions/evaluations.jsonl", nil, caller{}); w.Code == 200 {
		t.Fatal("unauthenticated export")
	}
}

func TestEvaluationExportFailureDoesNotSendPartialDownload(t *testing.T) {
	h := decisionEvaluationHandler(func(_ context.Context, w io.Writer) error {
		io.WriteString(w, "{\"decision_id\":\"partial\"}\n")
		return errors.New("read failed")
	})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, "/api/decisions/evaluations.jsonl", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "partial") || w.Header().Get("Content-Disposition") != "" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
}
