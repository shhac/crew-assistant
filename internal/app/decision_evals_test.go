package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestDecisionEvaluationOnlyContextNeverEntersAssistantContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := core.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const marker = "EVALUATION_ONLY_ASSISTANT_MARKER"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO decision_evaluations(decision_id,resolved_at,payload) VALUES(?,?,?)", "evaluation-only", "2026-10-03", `{"context":"`+marker+`"}`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	a := New(core.NewService(store, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), Options{})
	raw, messages, err := a.chatContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), marker) {
		t.Fatal("evaluation entered assistant context")
	}
	for _, m := range messages {
		if strings.Contains(m.Content, marker) {
			t.Fatal("evaluation entered assistant messages")
		}
	}
}
