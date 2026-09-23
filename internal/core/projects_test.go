package core

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func canonicalDirectory(t *testing.T, path string) string {
	t.Helper()
	value, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestExistingProjectTracksDirectoriesAndRequiresContract(t *testing.T) {
	s, _ := fixture(t)
	repository := t.TempDir()
	marker := filepath.Join(repository, "source.txt")
	if err := os.WriteFile(marker, []byte("owner content"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Existing project", Directories: []string{repository, filepath.Join(repository, ".")}})
	if err != nil {
		t.Fatal(err)
	}
	if p.ContractDefined || p.Status != "ready" || !reflect.DeepEqual(p.Directories, []string{canonicalDirectory(t, repository)}) {
		t.Fatalf("project=%+v", p)
	}
	if p.ScratchDirectory != filepath.Join(s.StateDirectory(), "projects", p.ID) {
		t.Fatalf("scratch outside selected state: %q", p.ScratchDirectory)
	}
	info, err := os.Stat(p.ScratchDirectory)
	if err != nil || !info.IsDir() {
		t.Fatalf("scratch: %v %v", info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatalf("scratch permissions %v", info.Mode())
	}
	in := DelegateInput{ProjectID: p.ID, ProfileID: "test", Role: "worker", Task: "Implement scoped change", AcceptanceCriteria: "Evidence reviewed"}
	if _, err = s.Delegate(testContext, in); err == nil || !strings.Contains(err.Error(), "acceptance") {
		t.Fatalf("draft was commissioned: %v", err)
	}
	p, err = s.RefineProject(testContext, p.ID, "Implement the requested change", "Tests demonstrate requested behavior")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delegate(testContext, in); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(repository)
	if err != nil || len(entries) != 1 {
		t.Fatalf("linked repository changed: %v %v", entries, err)
	}
	content, _ := os.ReadFile(marker)
	if string(content) != "owner content" {
		t.Fatal("source contents changed")
	}
}

func TestProjectDirectoriesPersistenceAndCanonicalDatabaseLocation(t *testing.T) {
	actualState := t.TempDir()
	database := filepath.Join(actualState, "selected.db")
	st, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(st, config.Default())
	first, err := s.CreateProject(testContext, ProjectInput{Title: "Tracked", Directories: []string{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	second := canonicalDirectory(t, t.TempDir())
	want, err := s.SetProjectDirectories(testContext, first.ID, []string{second})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "alias.db")
		if err := os.Symlink(database, alias); err != nil {
			t.Fatal(err)
		}
		database = alias
	}
	st, err = Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s = NewService(st, config.Default())
	if s.StateDirectory() != canonicalDirectory(t, actualState) {
		t.Fatalf("state derived from alias/config: %q", s.StateDirectory())
	}
	snapshot, err := s.Snapshot(testContext)
	if err != nil || len(snapshot.Projects) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	got := snapshot.Projects[0]
	if got.ScratchDirectory != want.ScratchDirectory || !reflect.DeepEqual(got.Directories, want.Directories) {
		t.Fatalf("lost paths: %+v", got)
	}
}

func TestInvalidProjectDirectoriesDoNotMutateContract(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, []byte("fixture"), 0600)
	inputs := [][]string{{"relative"}, {""}, {file}, {filepath.Join(t.TempDir(), "missing")}, {s.StateDirectory()}, {filepath.Dir(s.StateDirectory())}, make([]string, 17)}
	for _, directories := range inputs {
		if _, err := s.SetProjectDirectories(testContext, p.ID, directories); err == nil {
			t.Fatalf("accepted %q", directories)
		}
		if _, err := s.RefineProjectWithDirectories(testContext, p.ID, "replacement", "replacement", directories); err == nil {
			t.Fatalf("refinement accepted %q", directories)
		}
	}
	snapshot, _ := s.Snapshot(testContext)
	if snapshot.Projects[0].AcceptanceCriteria != p.AcceptanceCriteria || len(snapshot.Projects[0].Directories) != 0 {
		t.Fatal("invalid update partially changed project")
	}
}

func TestProjectDirectorySymlinksDeduplicateAndScratchSymlinksFailClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	s, _ := fixture(t)
	repository := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(repository, alias); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Existing", Directories: []string{alias, repository}})
	if err != nil || len(p.Directories) != 1 || p.Directories[0] != canonicalDirectory(t, repository) {
		t.Fatalf("project=%+v err=%v", p, err)
	}
	// Existing project ID paths must be checked too, including on restart.
	if err := os.Remove(p.ScratchDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repository, p.ScratchDirectory); err != nil {
		t.Fatal(err)
	}
	if err := s.store.prepareProject(&p); err == nil {
		t.Fatal("followed project scratch symlink")
	}
	os.Remove(p.ScratchDirectory)
	if err := os.Remove(filepath.Dir(p.ScratchDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repository, filepath.Dir(p.ScratchDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(testContext, ProjectInput{Title: "Must fail"}); err == nil {
		t.Fatal("followed projects parent symlink")
	}
	entries, _ := os.ReadDir(repository)
	if len(entries) != 0 {
		t.Fatalf("wrote scratch into linked repository: %v", entries)
	}
}

func TestExistingRecordsDeriveScratchRatherThanTrustStoredPath(t *testing.T) {
	state := t.TempDir()
	db := filepath.Join(state, "state.db")
	st, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := st.update(testContext, func(v *Snapshot) error {
		v.Projects = append(v.Projects, Project{ID: "legacy-project", Title: "Legacy", Status: "ready", ScratchDirectory: outside})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, _ := st.Snapshot(testContext)
	if v.Projects[0].ScratchDirectory != filepath.Join(canonicalDirectory(t, state), "projects", "legacy-project") {
		t.Fatalf("trusted stored scratch: %+v", v.Projects[0])
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("modified untrusted stored scratch")
	}
}

func TestTitleOnlyAndDirectoryClearing(t *testing.T) {
	s, _ := fixture(t)
	p, err := s.CreateProject(testContext, ProjectInput{Title: "Remember this project"})
	if err != nil || p.ContractDefined {
		t.Fatalf("project=%+v err=%v", p, err)
	}
	p, err = s.SetProjectDirectories(testContext, p.ID, []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.SetProjectDirectories(testContext, p.ID, nil)
	if err != nil || p.Directories == nil || len(p.Directories) != 0 {
		t.Fatalf("clear=%+v err=%v", p, err)
	}
}
