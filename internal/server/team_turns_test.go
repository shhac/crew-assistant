package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func TestTeamTurnRoutesIdentityAttributionAndAccounting(t *testing.T) {
	a, call := ownerApp(t)
	ctx := context.Background()
	var p core.Project
	w := call("POST", "/api/projects", `{"title":"History","brief":{"goal":"Write","criteria":["Complete"]},"template":"draft"}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	var task core.Task
	w = call("POST", "/api/projects/"+p.ID+"/tasks", `{"objective":"Write it","criteria":[]}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	m, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Recorded name", Kind: core.RoleImplementer, Engine: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	taskPath := "/api/projects/" + p.ID + "/tasks/" + task.ID + "/team-turns"
	memberPath := "/api/members/" + m.ID + "/team-turns"
	read := func(path string) turnHistoryResponse {
		t.Helper()
		w := call("GET", path, "")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{"claim_token", "launch_dir", "untracked_launch", "secret-claim", "private-launch"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("private field", w.Body.String())
			}
		}
		var h turnHistoryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	for _, path := range []string{taskPath, memberPath} {
		h := read(path)
		if len(h.Turns) != 0 || h.Aggregate.CacheReadShare != nil {
			t.Fatal(h)
		}
	}
	reasons := []string{core.FreshNoThread, core.FreshEngineChanged, core.FreshModelChanged, core.FreshOwnerRequested, core.FreshHarnessIncompatible, core.FreshHarnessUnavailable}
	base := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		turn := core.TeamTurn{ID: fmt.Sprintf("attempt-%02d", i), ProjectID: p.ID, TaskID: task.ID, MemberID: m.ID, MemberName: m.Name, Role: core.RoleImplementer, Seat: "Original seat", Engine: "codex", AdmittedAt: base.Add(time.Duration(i) * time.Second), ClaimToken: "secret-claim", LaunchDir: "private-launch"}
		if i == 1 {
			turn.Model = "configured-model"
		}
		if err := a.Core.AdmitTeamTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
		if i == 9 {
			continue
		}
		opening := core.TeamTurnOpening{At: turn.AdmittedAt, Resumed: i >= 6}
		if i < len(reasons) {
			opening.FreshReason = reasons[i]
		}
		if err := a.Core.OpenTeamTurn(ctx, turn.ID, opening); err != nil {
			t.Fatal(err)
		}
		if i == 8 {
			continue
		}
		if err := a.Core.AcceptTeamTurn(ctx, turn.ID, turn.AdmittedAt); err != nil {
			t.Fatal(err)
		}
		if i == 7 {
			continue
		}
		usage := session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 100, CacheRead: 20, CacheWrite: 10, Output: 5}, Final: true}
		if i == 1 {
			usage.Input = 900
			usage.CacheRead = 90
		}
		if i == 2 {
			usage = session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true}, Final: true}
		}
		if i == 3 {
			usage.CacheKnown = false
		}
		if i == 4 {
			usage = session.Usage{}
		}
		if i == 5 {
			usage.Final = false
		}
		terminal := core.TeamTurnTerminal{At: turn.AdmittedAt, Outcome: "failed", Usage: usage, Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 999}}, CompactionUsage: session.Usage{Usage: harness.Usage{Known: true, Input: 500}, Final: true}}
		if err := a.Core.FinishTeamTurn(ctx, turn.ID, terminal); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := a.Core.RecoverTeamTurn(ctx, turn.ID, false, turn.AdmittedAt); err != nil {
				t.Fatal(err)
			}
		}
	}
	taskHistory := read(taskPath)
	memberHistory := read(memberPath)
	if !reflect.DeepEqual(taskHistory, memberHistory) {
		t.Fatal("views disagree")
	}
	if len(taskHistory.Turns) != 10 || taskHistory.Aggregate.MeasuredTurns != 4 || taskHistory.Aggregate.Input != 1100 || taskHistory.Aggregate.CacheRead != 130 {
		t.Fatal(taskHistory.Aggregate)
	}
	for _, v := range taskHistory.Turns {
		if v.MemberName != m.Name || v.MemberID != m.ID || v.TaskID != task.ID || v.ProjectID != p.ID || v.Seat != "Original seat" {
			t.Fatal(v)
		}
		var i int
		fmt.Sscanf(v.ID, "attempt-%02d", &i)
		if v.ProviderDefault != (i != 1) || (i == 1 && v.Model != "configured-model") {
			t.Fatal(v)
		}
		if i < 6 && (v.Opening == nil || v.Opening.Resumed || v.Opening.FreshReason != reasons[i]) {
			t.Fatal(v)
		}
		if i == 6 && !v.Opening.Resumed {
			t.Fatal(v)
		}
		if i == 9 && (v.Opening != nil || v.Lifecycle != "admitted") {
			t.Fatal(v)
		}
		if i == 8 && v.Lifecycle != "opened" || i == 7 && v.Lifecycle != "accepted" {
			t.Fatal(v)
		}
		if i == 2 && (v.Terminal.Usage.Input == nil || *v.Terminal.Usage.Input != 0 || *v.Terminal.Usage.CacheRead != 0) {
			t.Fatal(v)
		}
		if i == 3 && v.Terminal.Usage.CacheRead != nil || i == 4 && v.Terminal.Usage.Input != nil {
			t.Fatal(v)
		}
		if i <= 6 && v.Terminal.Observed.Status != "partial" || i == 5 && v.Terminal.Usage.Status != "partial" {
			t.Fatal(v)
		}
		if i == 0 && (!v.Held || v.Terminal.Outcome != "failed") {
			t.Fatal(v)
		}
	}
	page := read(taskPath + "?limit=2")
	older := read(taskPath + "?limit=2&before=" + page.NextBefore)
	if !reflect.DeepEqual(page.Aggregate, older.Aggregate) || !reflect.DeepEqual(page.Aggregate, taskHistory.Aggregate) {
		t.Fatal("page-local aggregate")
	}
	// Project-only PM and release attempts span projects without invented tasks.
	for _, role := range []string{core.RolePM, core.RoleQA} {
		turn := core.TeamTurn{ID: role, ProjectID: "recorded-other-project", MemberID: m.ID, MemberName: m.Name, Role: role, Seat: role, Engine: "claude", AdmittedAt: base.Add(time.Minute)}
		if err := a.Core.AdmitTeamTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Core.SaveMember(ctx, m.ID, core.MemberInput{Name: "Renamed", Kind: core.RoleQA, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
	all := read(memberPath)
	if len(all.Turns) != 12 || len(read(taskPath).Turns) != 10 {
		t.Fatal(all)
	}
	for _, v := range all.Turns[:2] {
		if v.TaskID != "" || v.ProjectID != "recorded-other-project" || v.MemberName != m.Name {
			t.Fatal(v)
		}
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := snap.FindTask(task.ID)
	if !ok {
		t.Fatal("task disappeared")
	}
	readable := resolved.Ref
	if h := read("/api/projects/" + p.ID + "/tasks/" + readable + "/team-turns"); !reflect.DeepEqual(h, read(taskPath)) {
		t.Fatal("readable task differs")
	}
	var other core.Project
	w = call("POST", "/api/projects", `{"title":"Other","brief":{"goal":"Write","criteria":["Complete"]},"template":"draft"}`)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &other) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("GET", "/api/projects/"+other.ID+"/tasks/"+task.ID+"/team-turns", ""); w.Code != 404 {
		t.Fatal("cross-project task", w.Code)
	}
	for _, path := range []string{"/api/projects/missing/tasks/" + task.ID + "/team-turns", "/api/projects/" + p.ID + "/tasks/missing/team-turns", "/api/members/missing/team-turns"} {
		if w := call("GET", path, ""); w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, q := range []string{"limit=0", "limit=-1", "limit=201", "limit=999999999999999999999999", "limit=bad", "limit=", "limit=1&limit=2", "limit=%xx", "before=missing", "before=pm", "before=", "before=x&before=y"} {
		if w := call("GET", taskPath+"?"+q, ""); w.Code != 400 {
			t.Fatal(q, w.Code, w.Body.String())
		}
	}
	if err := a.Core.DeleteMember(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", memberPath, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if len(read(taskPath).Turns) != 10 {
		t.Fatal("deleted attribution")
	}
}

type failedHistoryWriter struct{ header http.Header }

func (w *failedHistoryWriter) Header() http.Header       { return w.header }
func (w *failedHistoryWriter) WriteHeader(int)           {}
func (w *failedHistoryWriter) Write([]byte) (int, error) { return 0, errors.New("closed response") }

func TestTeamTurnHistoryReadFailuresDoNotWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	service := core.NewService(store, cfg)
	a := app.New(service, cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, err := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	ctx := context.Background()
	m, err := service.SaveMember(ctx, "", core.MemberInput{Name: "Synthetic", Kind: core.RolePM, Engine: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	turn := core.TeamTurn{ID: "unchanged", ProjectID: "recorded-project", MemberID: m.ID, Role: core.RolePM, Seat: "PM", Engine: "codex", AdmittedAt: time.Now().UTC()}
	if err := service.AdmitTeamTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	path := "/api/members/" + m.ID + "/team-turns"
	request := func() *http.Request {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8340"+path, nil)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		return r
	}
	h.ServeHTTP(&failedHistoryWriter{header: http.Header{}}, request())
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request().WithContext(cancelled))
	if w.Code != 500 || strings.Contains(w.Body.String(), "context") {
		t.Fatal(w.Code, w.Body.String())
	}
	turns, err := service.TeamTurns(ctx, core.TeamTurnFilter{})
	if err != nil || len(turns) != 1 || !reflect.DeepEqual(turns[0], turn) {
		t.Fatal("read mutated record", turns, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request())
	if w.Code != 500 || strings.Contains(w.Body.String(), "sql") || strings.Contains(w.Body.String(), "turns") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTeamTurnRoutesOwnerAuthentication(t *testing.T) {
	a, auth, h := newDashboard(t, config.Default())
	m, err := a.Core.SaveMember(context.Background(), "", core.MemberInput{Name: "Synthetic", Kind: core.RolePM, Engine: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	created := send(h, auth, "POST", "/api/projects", strings.NewReader(`{"title":"Auth","brief":{"goal":"Write","criteria":["Complete"]},"template":"draft"}`), asOwner)
	var p core.Project
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &p) != nil {
		t.Fatal(created.Body.String())
	}
	created = send(h, auth, "POST", "/api/projects/"+p.ID+"/tasks", strings.NewReader(`{"objective":"Write","criteria":[]}`), asOwner)
	var task core.Task
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &task) != nil {
		t.Fatal(created.Body.String())
	}
	for _, path := range []string{"/api/members/" + m.ID + "/team-turns", "/api/projects/" + p.ID + "/tasks/" + task.ID + "/team-turns"} {
		request := func(token, host string, cookie *http.Cookie) *httptest.ResponseRecorder {
			r := httptest.NewRequest("GET", "http://"+host+path, nil)
			r.RemoteAddr = "127.0.0.1:4321"
			if token != "" {
				r.Header.Set("Authorization", "Bearer "+token)
			}
			if cookie != nil {
				r.AddCookie(cookie)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w
		}
		for _, token := range []string{"", "invalid"} {
			if w := request(token, "127.0.0.1:8340", nil); w.Code != 401 || strings.Contains(w.Body.String(), "turns") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		if w := request(auth.admin, "evil.example", nil); w.Code != 403 {
			t.Fatal(w.Code)
		}
		if w := request(auth.admin, "127.0.0.1:8340", nil); w.Code != 200 {
			t.Fatal(w.Code)
		}
		code, err := Pair(filepath.Dir(auth.pairingPath))
		if err != nil {
			t.Fatal(err)
		}
		login := send(h, auth, "POST", "/api/session", strings.NewReader(`{"token":"`+code+`"}`), caller{csrf: true})
		if login.Code != 200 {
			t.Fatal(login.Body.String())
		}
		cookie := login.Result().Cookies()[0]
		if w := request("", "127.0.0.1:8340", cookie); w.Code != 200 {
			t.Fatal(w.Code)
		}
		auth.mu.Lock()
		auth.sessions[cookie.Value] = time.Now().Add(-time.Minute)
		auth.mu.Unlock()
		if w := request("", "127.0.0.1:8340", cookie); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}
