package toolkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/integrations/connections"
)

// fixture is a machine with a fake Homebrew prefix and home: nothing real is
// run, fetched or installed.
type fixture struct {
	t       *testing.T
	prefix  string
	home    string
	now     time.Time
	mu      sync.Mutex
	runs    []Command
	fetches []string
	// versions is what each binary's --version prints, by name.
	versions map[string]string
	// formulas is each formula's version on the tap; absent ones fail.
	formulas map[string]string
	manifest string
	// job runs in place of an install, update, skill or verify command.
	job func(context.Context, Command) error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, prefix: t.TempDir(), home: t.TempDir(), now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), versions: map[string]string{}, formulas: map[string]string{}, manifest: `{}`}
	f.binary("brew")
	return f
}

func (f *fixture) binary(name string) string {
	f.t.Helper()
	path := filepath.Join(f.prefix, "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *fixture) skill(dir, name string) {
	f.t.Helper()
	path := filepath.Join(f.home, dir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) lock(body string) {
	f.t.Helper()
	path := filepath.Join(f.home, ".agents", ".skill-lock.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) manager() *Manager {
	return New(Options{
		Run: func(ctx context.Context, c Command) error {
			f.mu.Lock()
			f.runs = append(f.runs, c)
			version, probed := f.versions[filepath.Base(c.Path)]
			job := f.job
			f.mu.Unlock()
			if len(c.Args) == 1 && c.Args[0] == "--version" {
				if !probed {
					return errors.New("no such program")
				}
				fmt.Fprintln(c.Output, version)
				return nil
			}
			if job == nil {
				return nil
			}
			return job(ctx, c)
		},
		Fetch: func(_ context.Context, url string) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fetches = append(f.fetches, url)
			if url == SkillsManifestURL {
				return []byte(f.manifest), nil
			}
			name := strings.TrimSuffix(filepath.Base(url), ".rb")
			version, ok := f.formulas[name]
			if !ok {
				return nil, errors.New("update source returned HTTP 404")
			}
			return []byte(`url "https://github.com/shhac/` + name + `/releases/download/v` + version + `/` + name + `.tar.gz"`), nil
		},
		LookPath:   func(string) (string, error) { return "", errors.New("not on PATH") },
		Executable: func() (string, error) { return "", errors.New("no executable") },
		LookupEnv: func(key string) (string, bool) {
			if key == "HOME" {
				return f.home, true
			}
			return "", false
		},
		Home:     f.home,
		Prefixes: []string{f.prefix},
		Now:      func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now },
	})
}

func (f *fixture) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fetches)
}

func (f *fixture) commands() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Command
	for _, c := range f.runs {
		if len(c.Args) != 1 || c.Args[0] != "--version" {
			out = append(out, c)
		}
	}
	return out
}

func rowOf(t *testing.T, o Overview, id string) Row {
	t.Helper()
	for _, r := range o.Tools {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %s", id)
	return Row{}
}

func wait(t *testing.T, m *Manager, id string) JobView {
	t.Helper()
	m.mu.Lock()
	var done chan struct{}
	for _, j := range m.jobs {
		if j.id == id {
			done = j.done
		}
	}
	m.mu.Unlock()
	if done == nil {
		t.Fatalf("no job %s", id)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("job never finished")
	}
	v, err := m.Job(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCatalogIsWellFormed(t *testing.T) {
	ids := map[string]bool{}
	skills := map[string]bool{}
	for _, tool := range Catalog() {
		if tool.ID == "" || ids[tool.ID] {
			t.Fatalf("duplicate or empty id %q", tool.ID)
		}
		ids[tool.ID] = true
		if tool.Formula != "shhac/tap/"+tool.ID {
			t.Errorf("%s: formula %q is not its shhac/tap formula", tool.ID, tool.Formula)
		}
		if tool.Name == "" || tool.Purpose == "" || len(tool.Purpose) > 100 {
			t.Errorf("%s: name %q, purpose %q", tool.ID, tool.Name, tool.Purpose)
		}
		if tool.Skill != "" {
			if skills[tool.Skill] {
				t.Errorf("%s: skill %s listed twice", tool.ID, tool.Skill)
			}
			skills[tool.Skill] = true
		}
		if tool.Verify != nil && (len(tool.Verify) < 2 || tool.Verify[0] == "") {
			t.Errorf("%s: verify %q needs a program and arguments", tool.ID, tool.Verify)
		}
		for _, arg := range tool.Verify {
			if slices.Contains([]string{"login", "add", "import-desktop", "remove", "logout"}, arg) {
				t.Errorf("%s: verify %q would change the sign-in", tool.ID, tool.Verify)
			}
		}
		for _, line := range tool.Setup {
			if strings.TrimSpace(line) == "" || strings.ContainsAny(line, "\n;&|") {
				t.Errorf("%s: setup %q isn't one plain command", tool.ID, line)
			}
		}
		if tool.Connection != (connections.Operations(tool.ID) != nil) {
			t.Errorf("%s: connection %v disagrees with the connection CLIs", tool.ID, tool.Connection)
		}
	}
	for _, id := range []string{"lin", "agent-slack", "agent-mongo", "agent-sql", "agent-dd", "agent-notion", "agent-fathom", "g2g"} {
		if !ids[id] {
			t.Errorf("catalog is missing %s", id)
		}
	}
}

func TestParseVersion(t *testing.T) {
	for output, want := range map[string]string{
		"lin version 0.36.4\n":          "v0.36.4",
		"git-hunk 0.20.0":               "v0.20.0",
		"agent-sql version v1.19.2 (x)": "v1.19.2",
		"g2g version 1.0.0-rc.1,":       "v1.0.0-rc.1",
		"command not found":             "",
		"":                              "",
	} {
		if got := parseVersion(output); got != want {
			t.Errorf("%q: got %q, want %q", output, got, want)
		}
	}
}

func TestListDetectsInstalledOutdatedMissingAndUnknown(t *testing.T) {
	f := newFixture(t)
	f.binary("lin")
	f.binary("agent-slack")
	f.binary("agent-dd")
	f.versions["lin"] = "lin version 0.36.4"
	f.versions["agent-slack"] = "agent-slack version 0.48.1"
	// agent-dd is installed but its version can't be read.
	f.formulas["lin"] = "0.37.0"
	f.formulas["agent-slack"] = "0.48.1"
	o := f.manager().List(context.Background(), false)
	if !o.Homebrew.Available || o.Homebrew.Prefix != f.prefix || o.Homebrew.Install != "" {
		t.Fatalf("homebrew: %+v", o.Homebrew)
	}
	if len(o.Tools) != len(catalog) || o.CheckedAt == nil {
		t.Fatalf("rows %d, checked %v", len(o.Tools), o.CheckedAt)
	}
	lin := rowOf(t, o, "lin")
	if lin.Status != StatusOutdated || lin.Installed != "v0.36.4" || lin.Latest != "v0.37.0" || lin.Path != filepath.Join(f.prefix, "bin", "lin") || lin.Detail != "" {
		t.Fatalf("lin: %+v", lin)
	}
	if lin.Install != "brew install shhac/tap/lin" || lin.Update != "brew upgrade shhac/tap/lin" || !lin.Verify || lin.VerifyCommand != "lin user me" || !lin.Connection {
		t.Fatalf("lin commands: %+v", lin)
	}
	if slack := rowOf(t, o, "agent-slack"); slack.Status != StatusInstalled || slack.Latest != "v0.48.1" {
		t.Fatalf("slack: %+v", slack)
	}
	if dd := rowOf(t, o, "agent-dd"); dd.Status != StatusUnknown || dd.Installed != "" || dd.Detail == "" {
		t.Fatalf("dd: %+v", dd)
	}
	// The tap couldn't be read for agent-mongo: missing is still known.
	if mongo := rowOf(t, o, "agent-mongo"); mongo.Status != StatusMissing || mongo.Latest != "" || mongo.Path != "" {
		t.Fatalf("mongo: %+v", mongo)
	}
}

func TestListWithoutHomebrewOffersTheOfficialInstructions(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.prefix, "bin", "brew")); err != nil {
		t.Fatal(err)
	}
	m := f.manager()
	o := m.List(context.Background(), false)
	if o.Homebrew.Available || o.Homebrew.Install != HomebrewInstall {
		t.Fatalf("homebrew: %+v", o.Homebrew)
	}
	if _, err := m.Start("lin", ActionInstall); !errors.Is(err, ErrNoHomebrew) {
		t.Fatalf("install without Homebrew: %v", err)
	}
	if len(f.commands()) != 0 {
		t.Fatal("ran a command without Homebrew", f.commands())
	}
}

func TestHomebrewPrefixFollowsTheDaemonsOwnInstall(t *testing.T) {
	f := newFixture(t)
	exe := filepath.Join(f.prefix, "Cellar", "crew-assistant", "1.0.0", "bin", "crew-assistant")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	m := f.manager()
	m.opts.Prefixes = []string{}
	m.opts.Executable = func() (string, error) { return exe, nil }
	want, _ := filepath.EvalSymlinks(f.prefix)
	if got := m.homebrewPrefix(); got != want {
		t.Fatalf("prefix %q, want %q", got, want)
	}
}

func TestSkillStatusFromTheLockAndManifest(t *testing.T) {
	f := newFixture(t)
	f.manifest = `{"lin":{"version":"0.36.5","synced_at":"2026-09-01T00:00:00Z"},"agent-slack":{"version":"0.47.3","synced_at":"2026-08-01T00:00:00Z"}}`
	f.skill(".agents", "lin")
	f.skill(".claude", "agent-slack")
	f.skill(".claude", "agent-dd")
	f.lock(`{"version":3,"skills":{
		"lin":{"source":"shhac/agent-skills","updatedAt":"2026-08-20T10:00:00Z"},
		"agent-slack":{"source":"shhac/agent-skills","updatedAt":"2026-08-20T10:00:00Z"}}}`)
	o := f.manager().List(context.Background(), false)
	lin := rowOf(t, o, "lin").Skill
	if lin.Status != StatusOutdated || lin.Installed != "2026-08-20" || lin.Latest != "v0.36.5" || lin.Command != "npx skills add shhac/agent-skills --skill lin --global" || lin.Runnable || lin.Run != "" {
		t.Fatalf("lin skill: %+v", lin)
	}
	if slack := rowOf(t, o, "agent-slack").Skill; slack.Status != StatusInstalled {
		t.Fatalf("slack skill: %+v", slack)
	}
	// Installed by other means: present, but with no date to compare.
	if dd := rowOf(t, o, "agent-dd").Skill; dd.Status != StatusInstalled || dd.Installed != "" {
		t.Fatalf("dd skill: %+v", dd)
	}
	if mongo := rowOf(t, o, "agent-mongo").Skill; mongo.Status != StatusMissing {
		t.Fatalf("mongo skill: %+v", mongo)
	}
	f.lock(`not json`)
	o = f.manager().List(context.Background(), false)
	if lin := rowOf(t, o, "lin").Skill; lin.Status != StatusUnknown {
		t.Fatalf("unreadable lock: %+v", lin)
	}
	if mongo := rowOf(t, o, "agent-mongo").Skill; mongo.Status != StatusMissing {
		t.Fatalf("unreadable lock, absent skill: %+v", mongo)
	}
}

func TestLatestVersionsAreCachedAndRefreshable(t *testing.T) {
	f := newFixture(t)
	f.formulas["lin"] = "0.36.4"
	f.manifest = `{"lin":{"version":"0.36.5","synced_at":"2026-09-01T00:00:00Z"}}`
	m := f.manager()
	m.List(context.Background(), false)
	reads := f.fetchCount()
	if reads != len(catalog)+1 {
		t.Fatalf("first list read %d sources", reads)
	}
	m.List(context.Background(), false)
	if f.fetchCount() != reads {
		t.Fatal("a fresh cache was read again")
	}
	m.List(context.Background(), true)
	if f.fetchCount() != 2*reads {
		t.Fatal("refresh didn't read again")
	}
	f.mu.Lock()
	f.now = f.now.Add(LatestTTL + time.Minute)
	f.mu.Unlock()
	m.List(context.Background(), false)
	if f.fetchCount() != 3*reads {
		t.Fatal("a stale cache wasn't read again")
	}
}

func TestFailedReadsAreRetriedSooner(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	o := m.List(context.Background(), false)
	if rowOf(t, o, "lin").Latest != "" {
		t.Fatal("latest from nowhere")
	}
	reads := f.fetchCount()
	m.List(context.Background(), false)
	if f.fetchCount() != reads {
		t.Fatal("retried at once")
	}
	f.mu.Lock()
	f.now = f.now.Add(failedRetry + time.Second)
	f.formulas["lin"] = "0.36.4"
	f.manifest = `{"lin":{"version":"0.36.5"}}`
	f.mu.Unlock()
	if o = m.List(context.Background(), false); rowOf(t, o, "lin").Latest != "v0.36.4" {
		t.Fatal("failed read wasn't retried", rowOf(t, o, "lin"))
	}
}

func TestStartRefusesWhatTheCatalogDoesntOffer(t *testing.T) {
	f := newFixture(t)
	m := f.manager()
	for _, tc := range []struct {
		id, action string
		want       error
	}{
		{"curl", ActionInstall, ErrUnknownTool},
		{"../lin", ActionInstall, ErrUnknownTool},
		{"lin; rm -rf /", ActionUpdate, ErrUnknownTool},
		{"lin", "uninstall", ErrUnknownAction},
		{"lin", ActionUpdateAll, ErrUnknownAction},
		{"git-hunk", ActionVerify, ErrUnavailable},
		{"lin", ActionVerify, ErrNotInstalled},
		{"lin", ActionSkill, ErrNoNPX},
	} {
		if _, err := m.Start(tc.id, tc.action); !errors.Is(err, tc.want) {
			t.Errorf("%s %s: %v, want %v", tc.id, tc.action, err, tc.want)
		}
	}
	if len(f.commands()) != 0 {
		t.Fatal("a refused action ran", f.commands())
	}
}

func TestInstallRunsHomebrewAndReportsTheNewVersion(t *testing.T) {
	f := newFixture(t)
	// The install waits until the test has seen it start, so a fast machine
	// can't finish it first.
	release := make(chan struct{})
	f.job = func(_ context.Context, c Command) error {
		<-release
		fmt.Fprintf(c.Output, "==> Fetching shhac/tap/lin\nAuthorization: Bearer abcdefghijkl\r\nlin_api_secretvalue123 token=hunter2hunter2\n")
		f.binary("lin")
		f.mu.Lock()
		f.versions["lin"] = "lin version 0.36.4"
		f.mu.Unlock()
		return nil
	}
	m := f.manager()
	started, err := m.Start("lin", ActionInstall)
	if err != nil {
		t.Fatal(err)
	}
	if started.State != JobRunning || started.Command != "brew install shhac/tap/lin" || started.Tool != "lin" {
		t.Fatalf("started: %+v", started)
	}
	close(release)
	done := wait(t, m, started.ID)
	if done.State != JobSucceeded || done.Result != "lin v0.36.4 is installed." || done.FinishedAt == nil {
		t.Fatalf("done: %+v", done)
	}
	runs := f.commands()
	if len(runs) != 1 || runs[0].Path != filepath.Join(f.prefix, "bin", "brew") || !slices.Equal(runs[0].Args, []string{"install", "shhac/tap/lin"}) {
		t.Fatalf("ran %+v", runs)
	}
	if !slices.Contains(runs[0].Env, "PATH="+filepath.Join(f.prefix, "bin")+":"+filepath.Join(f.prefix, "sbin")+":/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("env %q", runs[0].Env)
	}
	log := strings.Join(done.Lines, "\n")
	if !strings.Contains(log, "==> Fetching shhac/tap/lin") || strings.Contains(log, "abcdefghijkl") || strings.Contains(log, "secretvalue") || strings.Contains(log, "hunter2") {
		t.Fatalf("log not redacted: %q", log)
	}
	if more, _ := m.Job(started.ID, done.Next); len(more.Lines) != 0 || more.Next != done.Next {
		t.Fatalf("read past the end: %+v", more)
	}
	if o := m.List(context.Background(), false); o.Job == nil || o.Job.ID != started.ID || len(o.Job.Lines) != 0 {
		t.Fatalf("overview job: %+v", o.Job)
	}
}

func TestOneJobAtATime(t *testing.T) {
	f := newFixture(t)
	f.binary("agent-slack")
	release := make(chan struct{})
	f.job = func(ctx context.Context, c Command) error {
		<-release
		return errors.New("exit status 1")
	}
	m := f.manager()
	first, err := m.Start("agent-slack", ActionUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("lin", ActionInstall); !errors.Is(err, ErrBusy) {
		t.Fatalf("second job: %v", err)
	}
	close(release)
	done := wait(t, m, first.ID)
	if done.State != JobFailed || !strings.Contains(done.Result, "Homebrew couldn't run") {
		t.Fatalf("failed update: %+v", done)
	}
	if !slices.Equal(f.commands()[0].Args, []string{"upgrade", "shhac/tap/agent-slack"}) {
		t.Fatal(f.commands())
	}
	if _, err := m.Start("lin", ActionInstall); err != nil {
		t.Fatalf("after the first finished: %v", err)
	}
	if _, err := m.Job("nope", 0); !errors.Is(err, ErrUnknownJob) {
		t.Fatal(err)
	}
}

func TestJobTimesOut(t *testing.T) {
	f := newFixture(t)
	f.job = func(ctx context.Context, c Command) error {
		<-ctx.Done()
		return ctx.Err()
	}
	m := f.manager()
	m.opts.InstallTimeout = 20 * time.Millisecond
	started, err := m.Start("lin", ActionInstall)
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, started.ID); done.State != JobFailed || done.Result != "Homebrew timed out." {
		t.Fatalf("%+v", done)
	}
}

func TestLogIsBoundedAndReadInPieces(t *testing.T) {
	j := &job{}
	for i := range logLines + 50 {
		fmt.Fprintf(j, "line %d\n", i)
	}
	fmt.Fprint(j, strings.Repeat("x", 5000)+"\n")
	v := j.view(0)
	if len(v.Lines) != logLines || v.Skipped != 51 || v.Next != logLines+51 {
		t.Fatalf("lines %d skipped %d next %d", len(v.Lines), v.Skipped, v.Next)
	}
	if last := v.Lines[len(v.Lines)-1]; len(last) > lineBytes+len("…") {
		t.Fatalf("line not clipped: %d", len(last))
	}
	if tail := j.view(v.Next - 2); len(tail.Lines) != 2 || tail.Skipped != 0 || tail.Lines[0] != fmt.Sprintf("line %d", logLines+49) {
		t.Fatalf("tail: %+v", tail.Lines)
	}
}

func TestSkillInstallUsesNPXWhenItCanBeFound(t *testing.T) {
	f := newFixture(t)
	npx := filepath.Join(f.home, ".nvm", "versions", "node", "v22.1.0", "bin", "npx")
	older := filepath.Join(f.home, ".nvm", "versions", "node", "v20.9.0", "bin", "npx")
	for _, path := range []string{npx, older} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.job = func(_ context.Context, c Command) error {
		f.skill(".agents", "agent-mongo")
		return nil
	}
	m := f.manager()
	if o := m.List(context.Background(), false); !o.NPX || rowOf(t, o, "agent-mongo").Skill.Run != "npx --yes skills add shhac/agent-skills --skill agent-mongo --global --yes" {
		t.Fatal("npx not found")
	}
	started, err := m.Start("agent-mongo", ActionSkill)
	if err != nil {
		t.Fatal(err)
	}
	if started.Command != "npx --yes skills add shhac/agent-skills --skill agent-mongo --global --yes" {
		t.Fatal(started.Command)
	}
	if done := wait(t, m, started.ID); done.State != JobSucceeded || done.Result != "The agent-mongo skill is installed." {
		t.Fatalf("%+v", done)
	}
	run := f.commands()[0]
	if run.Path != npx || !slices.Contains(run.Env, "PATH="+filepath.Dir(npx)+":/usr/bin:/bin:/usr/sbin:/sbin") {
		t.Fatalf("ran %+v", run)
	}
}

func TestVerifyReportsSignIn(t *testing.T) {
	f := newFixture(t)
	f.binary("agent-notion")
	f.binary("g2g")
	m := f.manager()
	f.job = func(_ context.Context, c Command) error { return nil }
	started, err := m.Start("agent-notion", ActionVerify)
	if err != nil {
		t.Fatal(err)
	}
	if done := wait(t, m, started.ID); done.State != JobSucceeded || done.Result != "Signed in." || done.Command != "agent-notion auth status" {
		t.Fatalf("%+v", done)
	}
	if run := f.commands()[0]; run.Path != filepath.Join(f.prefix, "bin", "agent-notion") || !slices.Equal(run.Args, []string{"auth", "status"}) {
		t.Fatalf("ran %+v", run)
	}
	// g2g signs in through gh, which isn't installed here.
	if _, err := m.Start("g2g", ActionVerify); !errors.Is(err, ErrNotInstalled) || !strings.Contains(err.Error(), "gh") {
		t.Fatalf("g2g without gh: %v", err)
	}
}

func TestUpdateAllUpgradesOnlyOutdatedTools(t *testing.T) {
	f := newFixture(t)
	for name, versions := range map[string][2]string{"lin": {"0.36.4", "0.37.0"}, "agent-sql": {"1.19.2", "1.20.0"}, "agent-slack": {"0.48.1", "0.48.1"}} {
		f.binary(name)
		f.versions[name] = name + " version " + versions[0]
		f.formulas[name] = versions[1]
	}
	// Found only on PATH: Homebrew didn't install it, so it can't upgrade it.
	f.versions["agent-dd"] = "agent-dd version 0.1.0"
	f.formulas["agent-dd"] = "0.20.1"
	m := f.manager()
	elsewhere := filepath.Join(t.TempDir(), "agent-dd")
	m.opts.LookPath = func(name string) (string, error) {
		if name == "agent-dd" {
			return elsewhere, nil
		}
		return "", errors.New("not on PATH")
	}
	o := m.List(context.Background(), false)
	want := "brew upgrade shhac/tap/lin shhac/tap/agent-sql"
	if o.UpdateAll != want {
		t.Fatalf("update all %q", o.UpdateAll)
	}
	if _, err := m.UpdateAll(context.Background(), "brew upgrade shhac/tap/lin"); !errors.Is(err, ErrChanged) {
		t.Fatalf("a different confirmed list: %v", err)
	}
	if len(f.commands()) != 0 {
		t.Fatal("ran an unconfirmed list", f.commands())
	}
	started, err := m.UpdateAll(context.Background(), o.UpdateAll)
	if err != nil {
		t.Fatal(err)
	}
	if started.Command != want || started.Action != ActionUpdateAll {
		t.Fatalf("%+v", started)
	}
	if done := wait(t, m, started.ID); done.State != JobSucceeded || done.Result != "Updated lin, agent-sql." {
		t.Fatalf("%+v", done)
	}
	if run := f.commands()[0]; run.Path != filepath.Join(f.prefix, "bin", "brew") || !slices.Equal(run.Args, []string{"upgrade", "shhac/tap/lin", "shhac/tap/agent-sql"}) {
		t.Fatalf("ran %+v", run)
	}
	f.mu.Lock()
	f.versions["lin"] = "lin version 0.37.0"
	f.versions["agent-sql"] = "agent-sql version 1.20.0"
	f.mu.Unlock()
	if o = m.List(context.Background(), false); o.UpdateAll != "" {
		t.Fatalf("nothing outdated, but %q", o.UpdateAll)
	}
	if _, err := m.UpdateAll(context.Background(), want); !errors.Is(err, ErrUpToDate) {
		t.Fatalf("nothing outdated: %v", err)
	}
}

func TestUpdatingACopyOutsideHomebrewInstallsHomebrews(t *testing.T) {
	f := newFixture(t)
	f.versions["lin"] = "lin version 0.36.4"
	f.formulas["lin"] = "0.37.0"
	m := f.manager()
	elsewhere := filepath.Join(t.TempDir(), "lin")
	m.opts.LookPath = func(name string) (string, error) {
		if name == "lin" {
			return elsewhere, nil
		}
		return "", errors.New("not on PATH")
	}
	lin := rowOf(t, m.List(context.Background(), false), "lin")
	if lin.Status != StatusOutdated || lin.Path != elsewhere || lin.Update != "brew install shhac/tap/lin" || lin.Detail == "" {
		t.Fatalf("lin: %+v", lin)
	}
	started, err := m.Start("lin", ActionUpdate)
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, started.ID)
	if started.Command != lin.Update || !slices.Equal(f.commands()[0].Args, []string{"install", "shhac/tap/lin"}) {
		t.Fatalf("shown %q, ran %q", started.Command, f.commands()[0].Args)
	}
}

// Each process gets only the keys it needs, never the daemon's own.
func TestCommandsGetOnlyTheirAllowedEnvironment(t *testing.T) {
	f := newFixture(t)
	f.binary("agent-notion")
	f.binary("npx")
	f.versions["agent-notion"] = "agent-notion version 0.10.2"
	m := f.manager()
	m.opts.LookupEnv = func(key string) (string, bool) { return "value-of-" + key, true }
	for _, start := range []func() (JobView, error){
		func() (JobView, error) { return m.Start("agent-notion", ActionVerify) },
		func() (JobView, error) { return m.Start("agent-notion", ActionUpdate) },
		func() (JobView, error) { return m.Start("agent-notion", ActionSkill) },
	} {
		started, err := start()
		if err != nil {
			t.Fatal(err)
		}
		wait(t, m, started.ID)
	}
	m.List(context.Background(), false)
	f.mu.Lock()
	runs := append([]Command{}, f.runs...)
	f.mu.Unlock()
	if len(runs) < 4 {
		t.Fatalf("ran %d commands", len(runs))
	}
	allowed := []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "SHELL", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "HOMEBREW_NO_ENV_HINTS", "HOMEBREW_NO_COLOR"}
	for _, run := range runs {
		if run.Env == nil {
			t.Fatalf("%s has a nil environment, which exec reads as the daemon's", run.Path)
		}
		for _, entry := range run.Env {
			if key, _, _ := strings.Cut(entry, "="); !slices.Contains(allowed, key) {
				t.Errorf("%s %v got %s", run.Path, run.Args, key)
			}
		}
	}
}

func TestRunNeverHandsOverTheDaemonsEnvironment(t *testing.T) {
	t.Setenv("TOOLKIT_DAEMON_SECRET", "kept-out")
	var out clipped
	if err := run(context.Background(), Command{Path: "/usr/bin/env", Output: &out}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out.data), "kept-out") {
		t.Fatalf("child saw the daemon's environment: %q", out.data)
	}
}

func TestFailureOutcomes(t *testing.T) {
	exit := exec.Command("/bin/sh", "-c", "exit 3").Run()
	for _, tc := range []struct {
		name          string
		state, result string
		got           [2]string
	}{
		{"brew failed, old version left", JobFailed, "Homebrew exited with status 3; lin v1.0.0 is installed.", pair(brewOutcome("lin", "v1.0.0", exit))},
		{"brew failed, nothing installed", JobFailed, "Homebrew exited with status 3.", pair(brewOutcome("lin", "", exit))},
		{"brew finished, no version", JobFailed, "Homebrew finished, but lin's version couldn't be read.", pair(brewOutcome("lin", "", nil))},
		{"not signed in", JobFailed, "Not signed in yet (lin exited with status 3). Run the setup commands in your terminal.", pair(verifyOutcome("lin", exit))},
		{"check timed out", JobFailed, "The check timed out.", pair(verifyOutcome("lin", context.DeadlineExceeded))},
	} {
		if tc.got != [2]string{tc.state, tc.result} {
			t.Errorf("%s: %q", tc.name, tc.got)
		}
	}
	f := newFixture(t)
	m := f.manager()
	if state, result := m.skillOutcome("lin", exit); state != JobFailed || result != "The skills CLI exited with status 3." {
		t.Errorf("skills CLI failed: %s", result)
	}
	if state, result := m.skillOutcome("lin", nil); state != JobFailed || !strings.Contains(result, "isn't in") {
		t.Errorf("skill missing afterwards: %s", result)
	}
}

func pair(state, result string) [2]string { return [2]string{state, result} }

func TestRedact(t *testing.T) {
	for _, secret := range []string{
		"ghp_abcdefghijklmnop", "github_pat_11ABCDEFG", "xoxb-1234-5678-abcdef", "xoxc-123-abc", "lin_api_abcdef123456",
		"sk_live_abcdefghijk", "ntn_abcdefghijklmnopqrstuvwxyz", "Authorization: Basic Zm9vOmJhcg==", "Bearer abcdefghijklmnop",
		"https://user:pa55word@example.com/", "api_key=abcdef123", `"password": "hunter22"`, "AKIAABCDEFGHIJKLMNOP",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N",
	} {
		if got := Redact("before " + secret + " after"); strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
			t.Errorf("%q kept: %q", secret, got)
		}
	}
	plain := "==> Upgrading shhac/tap/lin 0.36.4 -> 0.37.0"
	if got := Redact(plain); got != plain {
		t.Errorf("plain output changed: %q", got)
	}
}
