package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/toolkit"
)

// fakeToolkit is a Homebrew prefix with brew and lin in it; commands are
// recorded, never run.
func fakeToolkit(t *testing.T) (*toolkit.Manager, string, func() [][]string) {
	t.Helper()
	prefix := t.TempDir()
	for _, name := range []string{"brew", "lin"} {
		path := filepath.Join(prefix, "bin", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var runs [][]string
	m := toolkit.New(toolkit.Options{
		Run: func(_ context.Context, c toolkit.Command) error {
			if slices.Equal(c.Args, []string{"--version"}) {
				fmt.Fprintln(c.Output, filepath.Base(c.Path)+" version 0.36.4")
				return nil
			}
			mu.Lock()
			runs = append(runs, append([]string{c.Path}, c.Args...))
			mu.Unlock()
			fmt.Fprintln(c.Output, "==> Upgrading shhac/tap/lin\nghp_abcdefghijklmnop")
			return nil
		},
		Fetch: func(_ context.Context, url string) ([]byte, error) {
			if strings.HasSuffix(url, "/lin.rb") {
				return []byte(`url "https://github.com/shhac/lin/releases/download/v0.37.0/lin.tar.gz"`), nil
			}
			return nil, errors.New("update source could not be reached")
		},
		LookPath:   func(string) (string, error) { return "", errors.New("not on PATH") },
		Executable: func() (string, error) { return "", errors.New("none") },
		LookupEnv:  func(string) (string, bool) { return "", false },
		Home:       t.TempDir(),
		Prefixes:   []string{prefix},
	})
	return m, prefix, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return append([][]string{}, runs...)
	}
}

func TestToolkitRoutesAreOwnerOnlyAndRunOnlyCatalogCommands(t *testing.T) {
	a, auth, h := newDashboard(t, config.Default())
	m, prefix, runs := fakeToolkit(t)
	a.Toolkit = m
	if w := send(h, auth, "GET", "/api/toolkit", nil, caller{}); w.Code != 401 {
		t.Fatalf("unpaired list: %d", w.Code)
	}
	w := send(h, auth, "GET", "/api/toolkit", nil, caller{owner: true})
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var overview toolkit.Overview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if !overview.Homebrew.Available || len(overview.Tools) != len(toolkit.Catalog()) || overview.Tools[0].ID != "lin" || overview.Tools[0].Status != toolkit.StatusOutdated {
		t.Fatalf("overview: %+v", overview)
	}
	if w := send(h, auth, "POST", "/api/toolkit/lin/update", nil, caller{owner: true}); w.Code != 403 {
		t.Fatalf("cross-site update: %d", w.Code)
	}
	if w := send(h, auth, "POST", "/api/toolkit/lin/update", nil, caller{csrf: true}); w.Code != 401 {
		t.Fatalf("unpaired update: %d", w.Code)
	}
	for _, path := range []string{"/api/toolkit/curl/install", "/api/toolkit/lin/uninstall", "/api/toolkit/lin/update-all", "/api/toolkit/jobs/nope"} {
		if w := send(h, auth, "POST", path, nil, asOwner); w.Code != 404 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if len(runs()) != 0 {
		t.Fatal("a refused request ran", runs())
	}
	w = send(h, auth, "POST", "/api/toolkit/lin/update", nil, asOwner)
	if w.Code != 202 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var job toolkit.JobView
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.Command != "brew upgrade shhac/tap/lin" {
		t.Fatalf("job: %+v %v", job, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for job.State == toolkit.JobRunning && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		w = send(h, auth, "GET", "/api/toolkit/jobs/"+job.ID+"?after=0", nil, caller{owner: true})
		if w.Code != 200 {
			t.Fatalf("poll: %d %s", w.Code, w.Body.String())
		}
		job = toolkit.JobView{}
		_ = json.Unmarshal(w.Body.Bytes(), &job)
	}
	if job.State != toolkit.JobSucceeded || strings.Contains(w.Body.String(), "ghp_") || !slices.Contains(job.Lines, "==> Upgrading shhac/tap/lin") {
		t.Fatalf("finished: %s", w.Body.String())
	}
	if got := runs(); len(got) != 1 || !slices.Equal(got[0], []string{filepath.Join(prefix, "bin", "brew"), "upgrade", "shhac/tap/lin"}) {
		t.Fatalf("ran %q", got)
	}
	if w := send(h, auth, "GET", "/api/toolkit/jobs/"+job.ID+"?after=-1", nil, caller{owner: true}); w.Code != 400 {
		t.Fatalf("negative after: %d", w.Code)
	}
	if w := send(h, auth, "GET", "/api/toolkit/jobs/nope", nil, caller{owner: true}); w.Code != 404 {
		t.Fatalf("unknown job: %d", w.Code)
	}
	if w := send(h, auth, "POST", "/api/toolkit/git-hunk/verify", nil, asOwner); w.Code != 409 {
		t.Fatalf("no verify: %d %s", w.Code, w.Body.String())
	}
	if overview.UpdateAll != "brew upgrade shhac/tap/lin" {
		t.Fatalf("update all: %q", overview.UpdateAll)
	}
	for body, code := range map[string]int{
		`{"command":"brew upgrade shhac/tap/lin shhac/tap/curl"}`: 409,
		`{"command":"brew upgrade shhac/tap/lin"}`:                202,
		`{}`:                 409,
		`{"command":1}`:      400,
		`{"formulas":["x"]}`: 400,
	} {
		if w := send(h, auth, "POST", "/api/toolkit/update-all", strings.NewReader(body), asOwner); w.Code != code {
			t.Errorf("update all %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for deadline := time.Now().Add(10 * time.Second); len(runs()) < 2 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runs(); len(got) != 2 || !slices.Equal(got[1][1:], []string{"upgrade", "shhac/tap/lin"}) {
		t.Fatalf("ran %q", got)
	}
	a.Demo = true
	if w := send(h, auth, "GET", "/api/toolkit", nil, caller{owner: true}); w.Code != 403 {
		t.Fatalf("demo list: %d", w.Code)
	}
	if w := send(h, auth, "POST", "/api/toolkit/update-all", strings.NewReader(`{"command":"brew upgrade shhac/tap/lin"}`), asOwner); w.Code != 403 {
		t.Fatalf("demo update all: %d", w.Code)
	}
}
