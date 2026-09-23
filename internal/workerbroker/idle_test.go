package workerbroker

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func TestQuiesceRequiresTerminalRunsAndCompletedCleanup(t *testing.T) {
	for _, status := range []string{"queued", "running", "blocked", "interrupted"} {
		b := &Broker{state: diskState{Runs: map[string]*storedRun{"one": {Run: worker.Run{Status: status}}}}}
		if err := b.QuiesceIdle(); err == nil || b.quiesced {
			t.Fatal("quiesced outstanding run", status)
		}
	}
	b := &Broker{state: diskState{Runs: map[string]*storedRun{"one": {Run: worker.Run{Status: "completed"}}}}, active: map[string]context.CancelFunc{"one": func() {}}}
	if err := b.QuiesceIdle(); err == nil {
		t.Fatal("quiesced before cleanup")
	}
	delete(b.active, "one")
	if err := b.QuiesceIdle(); err != nil || !b.quiesced {
		t.Fatal(err)
	}
	// An already-authenticated client cannot enqueue work while the manager closes.
	b.cfg.ProjectID = "project"
	r := httptest.NewRequest("POST", "/runs", strings.NewReader(`{"project_id":"project","role":"worker","agent_id":"agent","dispatch_key":"dispatch","task":"task","acceptance_criteria":"evidence","capabilities":["review"],"prohibitions":["deployment","production_data_access","purchases"]}`))
	r.Header.Set("Idempotency-Key", "dispatch")
	w := httptest.NewRecorder()
	b.start(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/runs/one/resume", strings.NewReader(`{"instruction":"continue"}`))
	r.Header.Set("Idempotency-Key", "resume")
	r.SetPathValue("id", "one")
	w = httptest.NewRecorder()
	b.control(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
}
