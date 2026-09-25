package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/filesystem"
)

func TestFilesystemRequiresOwnerAndReturnsMetadataOnly(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(t.TempDir(), "config.json"), app.Options{})
	auth, err := NewAuth(t.TempDir(), "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	if err = os.WriteFile(filepath.Join(root, "note.txt"), []byte("private-file-content"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(path string, owner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8340"+path, nil)
		r.RemoteAddr = "127.0.0.1:4321"
		if owner {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	endpoint := "/api/filesystem?path=" + url.QueryEscape(root)
	if w := call(endpoint, false); w.Code != 401 {
		t.Fatalf("unauthorized directory listing: %d %s", w.Code, w.Body.String())
	}
	w := call(endpoint, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-file-content") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("metadata response: %d %s", w.Code, w.Body.String())
	}
	var result filesystem.Listing
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Entries) != 1 || result.Entries[0].Name != "note.txt" {
		t.Fatal(w.Body.String())
	}
	if w = call("/api/filesystem?path="+url.QueryEscape(filepath.Join(root, "note.txt")), true); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	if w = call(endpoint+"&hidden=invalid", true); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	a.Demo = true
	if w = call(endpoint, true); w.Code != 403 {
		t.Fatal("demo exposed host filesystem", w.Code)
	}
}

func TestExistingProjectDirectoriesHTTP(t *testing.T) {
	state := t.TempDir()
	source := t.TempDir()
	second := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(state, "config.json"), app.Options{})
	auth, err := NewAuth(t.TempDir(), "http://127.0.0.1:8340", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := New(a, auth)
	call := func(method, path string, body any, owner bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, strings.NewReader(string(data)))
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("X-Requested-With", "crew-assistant")
		if owner {
			r.Header.Set("Authorization", "Bearer "+auth.admin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/projects", map[string]any{"title": "Existing workspace", "directories": []string{source}}, true)
	if w.Code != 201 {
		t.Fatalf("intake: %d %s", w.Code, w.Body.String())
	}
	var p core.Project
	if err = json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(source)
	if p.Brief.Version != 0 || len(p.Directories) != 1 || p.Directories[0] != canonical || p.ScratchDirectory == "" {
		t.Fatalf("bad intake: %+v", p)
	}
	if entries, err := os.ReadDir(source); err != nil || len(entries) != 0 {
		t.Fatal("intake wrote to source", entries, err)
	}
	target := "/api/projects/" + p.ID + "/directories"
	if w = call("PUT", target, map[string]any{"directories": []string{second}}, false); w.Code != 401 {
		t.Fatal("unowned attachment allowed", w.Code)
	}
	w = call("PUT", target, map[string]any{"directories": []string{source, second}}, true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var updated core.Project
	if err = json.Unmarshal(w.Body.Bytes(), &updated); err != nil || len(updated.Directories) != 2 || updated.ScratchDirectory != p.ScratchDirectory {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/state", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Existing workspace") {
		t.Fatal(w.Body.String())
	}
}
