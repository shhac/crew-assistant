package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestChatQueueRoutesAuthenticateValidateAndRetryIdempotently(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	call := func(method, path, body string, authorized, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		if authorized {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		if csrf {
			r.Header.Set("X-Requested-With", "crew-assistant")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	drawing := []struct{ method, path, body string }{{"POST", "/api/members/m/avatar", `{"look":"Violet bob"}`}, {"POST", "/api/assistant/avatar", `{"look":"Silver hair"}`}}
	for _, route := range append([]struct{ method, path, body string }{{"POST", "/api/chat/messages", `{"id":"one","message":"Hello"}`}, {"GET", "/api/chat/turns", ""}, {"DELETE", "/api/chat/messages/one", ""}}, drawing...) {
		if w := call(route.method, route.path, route.body, false, true); w.Code != 401 {
			t.Fatal(route, w.Code, w.Body.String())
		}
	}
	for _, route := range append([]struct{ method, path, body string }{{"POST", "/api/chat/messages", `{"id":"one","message":"Hello"}`}}, drawing...) {
		if w := call(route.method, route.path, route.body, true, false); w.Code != 403 {
			t.Fatal(route, w.Code)
		}
	}
	for _, body := range []string{`{"id":"one","message":""}`, `{"id":"invalid.id","message":"hello"}`, `{"id":"one","message":"hello","extra":true}`} {
		if w := call("POST", "/api/chat/messages", body, true, true); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for i := 0; i < 2; i++ {
		if w := call("POST", "/api/chat/messages", `{"id":"one","message":"Hello"}`, true, true); w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call("POST", "/api/chat/messages", `{"id":"one","message":"Changed"}`, true, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := call("GET", "/api/chat/turns", "", true, true)
	var reply struct {
		Turns []core.ChatTurn `json:"turns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Turns) != 1 || reply.Turns[0].Status != "queued" {
		t.Fatal(reply)
	}
	if w := call("DELETE", "/api/chat/messages/one", "", true, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	for i := 0; i < 20; i++ {
		if _, err := s.EnqueueChat(context.Background(), fmt.Sprintf("pending-%d", i), "Queued"); err != nil {
			t.Fatal(err)
		}
	}
	if w := call("POST", "/api/chat/messages", `{"id":"extra","message":"Hello"}`, true, true); w.Code != 429 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/chat/messages", `{"id":"uncertain","message":"Hello"}`, true, true); w.Code != 500 {
		t.Fatal("storage failure must not look definitely rejected", w.Code, w.Body.String())
	}
}

// The queue routes exist so the owner can change what runs before it runs.
// They must refuse a stale intent rather than merging it.
func TestChatQueueHoldEditAndReorder(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := core.NewService(store, cfg)
	a := app.New(s, cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		r.Header.Set("X-Requested-With", "crew-assistant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, id := range []string{"one", "two", "three"} {
		if got := call("POST", "/api/chat/messages", `{"id":"`+id+`","message":"text `+id+`"}`); got.Code != 202 {
			t.Fatal(got.Body.String())
		}
	}

	// Decoded fresh each time: an absent "hold" key leaves a reused struct's
	// pointer untouched, which would hide a hold that was never released.
	type queueView struct {
		Turns    []core.ChatTurn `json:"turns"`
		Hold     *core.ChatHold  `json:"hold"`
		Revision int             `json:"revision"`
	}
	read := func() queueView {
		var out queueView
		if err := json.Unmarshal(call("GET", "/api/chat/turns", "").Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if read().Hold != nil {
		t.Fatal("a fresh queue reported a hold")
	}

	if got := call("POST", "/api/chat/messages/two/hold", `{"reason":"editing"}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if hold := read().Hold; hold == nil || hold.TurnID != "two" {
		t.Fatalf("the hold is not reported: %+v", hold)
	}

	// An edit against the wrong revision is refused.
	if got := call("PATCH", "/api/chat/messages/two", `{"message":"corrected","revision":99}`); got.Code != 409 {
		t.Fatalf("a stale edit returned %d, want 409", got.Code)
	}
	if got := call("PATCH", "/api/chat/messages/two", `{"message":"corrected","revision":0}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	// An empty edit is a validation failure, not a silent no-op.
	if got := call("PATCH", "/api/chat/messages/two", `{"message":"  ","revision":1}`); got.Code != 400 {
		t.Fatalf("an empty edit returned %d, want 400", got.Code)
	}

	if got := call("PUT", "/api/chat/queue", `{"order":["three","one","two"],"revision":999}`); got.Code != 409 {
		t.Fatalf("a stale reorder returned %d, want 409", got.Code)
	}
	if got := call("PUT", "/api/chat/queue", `{"order":["three","one","two"],"revision":`+strconv.Itoa(read().Revision)+`}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	after := read()
	if after.Turns[0].ID != "three" || after.Turns[2].ID != "two" {
		t.Fatalf("order = %s %s %s", after.Turns[0].ID, after.Turns[1].ID, after.Turns[2].ID)
	}
	if after.Turns[2].Message != "corrected" {
		t.Fatalf("the edit was lost: %q", after.Turns[2].Message)
	}

	if got := call("DELETE", "/api/chat/messages/two/hold", ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if read().Hold != nil {
		t.Fatal("the hold survived its release")
	}
}

func TestChatSuggestionRouteRefusesStaleRequestsAndReportsUnavailableModels(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	call := func(body string, authorized bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8340/api/chat/suggestion", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		if authorized {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		r.Header.Set("X-Requested-With", "crew-assistant")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call(`{"after":"reply"}`, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(`{"after":"reply","extra":true}`, true); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Nothing to follow yet: the conversation has not settled on that reply.
	if w := call(`{"after":"reply"}`, true); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg.Model.Engine = "openai-compatible"
	cfg.Model.Model = "local"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if w := call(`{"after":"reply"}`, true); w.Code != 503 || !strings.Contains(w.Body.String(), "no approved small model") {
		t.Fatal(w.Code, w.Body.String())
	}
}
