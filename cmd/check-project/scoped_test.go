package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScopedListFixture(t *testing.T) {
	if file := os.Getenv("CREW_REAL_SCOPE_FIXTURE"); file != "" {
		var log bytes.Buffer
		scope := scopedCheck(exec.Command, &log, file)
		if scope == nil {
			os.Stderr.WriteString(log.String())
			os.Exit(1)
		}
		json.NewEncoder(os.Stdout).Encode(scope.packages)
		os.Exit(0)
	}
	if data := os.Getenv("CREW_LIST_FIXTURE"); data != "" {
		os.Stdout.WriteString(data)
		os.Exit(0)
	}
}

func TestScopedDiscoveryUsesRealRepositoryAndSymlinkPaths(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "files")
	if err = os.WriteFile(file, []byte("README.md\ninternal/text/text.go\n"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "repository")
	if err = os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	binary, _ := os.Executable()
	cmd := exec.Command(binary, "-test.run=^TestScopedListFixture$")
	cmd.Dir = alias
	cmd.Env = append(os.Environ(), "PWD="+alias, "GOPROXY=off", "CREW_REAL_SCOPE_FIXTURE="+file)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real scoped discovery: %v\n%s", err, out)
	}
	var packages []string
	if err = json.Unmarshal(out, &packages); err != nil {
		t.Fatal(err, string(out))
	}
	found := false
	for _, pkg := range packages {
		if pkg == "github.com/shhac/crew-assistant/internal/text" {
			found = true
		}
	}
	if !found {
		t.Fatalf("changed package not selected: %v", packages)
	}
}

func TestScopedSelection(t *testing.T) {
	root, _ := os.Getwd()
	var listing bytes.Buffer
	for _, p := range []listedPackage{
		{Dir: filepath.Join(root, "lib"), ImportPath: "lib"},
		{Dir: filepath.Join(root, "direct"), ImportPath: "direct", Imports: []string{"lib"}},
		{Dir: filepath.Join(root, "consumer"), ImportPath: "consumer", TestImports: []string{"lib"}},
		{Dir: filepath.Join(root, "top"), ImportPath: "top", XTestImports: []string{"consumer"}},
		{Dir: filepath.Join(root, "internal/dashboard"), ImportPath: "dashboard"},
		{Dir: filepath.Join(root, "internal/config"), ImportPath: "config"},
		{Dir: filepath.Join(root, "configuser"), ImportPath: "configuser", Imports: []string{"config"}},
	} {
		json.NewEncoder(&listing).Encode(p)
	}
	binary, _ := os.Executable()
	factory := func(name string, args ...string) *exec.Cmd {
		if name != "go" || !reflect.DeepEqual(args, []string{"list", "-json", "./..."}) {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		cmd := exec.Command(binary, "-test.run=^TestScopedListFixture$")
		cmd.Env = append(os.Environ(), "CREW_LIST_FIXTURE="+listing.String())
		return cmd
	}
	for _, tc := range []struct {
		name, paths    string
		packages       []string
		frontend, full bool
	}{
		{"Go file", "lib/a.go", []string{"consumer", "direct", "lib", "top"}, false, false},
		{"package documentation", "lib/README.md", []string{"consumer", "direct", "lib", "top"}, false, false},
		{"embed", "lib/data/template.html", []string{"consumer", "direct", "lib", "top"}, false, false},
		{"testdata", "lib/testdata/example", []string{"consumer", "direct", "lib", "top"}, false, false},
		{"deleted package", "lib/deleted/file.go", []string{"consumer", "direct", "lib", "top"}, false, false},
		{"UI", "internal/dashboard/ui/src/App.svelte", nil, true, false},
		{"assets", "internal/dashboard/assets/app.js", []string{"dashboard"}, true, false},
		{"module", "go.mod", nil, false, true},
		{"Makefile", "Makefile", nil, false, true},
		{"runner", "cmd/check-project/main.go", nil, false, true},
		{"coverage", "internal/checktest/report.go", nil, false, true},
		{"empty", "", nil, false, false},
		{"top-level docs", "README.md\nAGENTS.md\nLICENSE\nCONTRIBUTING.md", nil, false, false},
		{"design docs", "design-docs/decisions/example.md", nil, false, false},
		{"example config", "config.example.json", []string{"config", "configuser"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "files")
			os.WriteFile(file, []byte(tc.paths), 0600)
			var out bytes.Buffer
			s := scopedCheck(factory, &out, file)
			if tc.full {
				if s != nil {
					t.Fatal(s)
				}
				return
			}
			if s == nil || !reflect.DeepEqual(s.packages, tc.packages) || s.frontend != tc.frontend {
				t.Fatalf("scope=%+v output=%s", s, &out)
			}
		})
	}
	if scopedCheck(factory, &listing, "") != nil {
		t.Fatal("missing record must run full")
	}
}

func TestScopedExecution(t *testing.T) {
	binary, _ := os.Executable()
	for _, result := range []string{"pass", "skip"} {
		var calls []string
		factory := func(name string, args ...string) *exec.Cmd {
			calls = append(calls, name+" "+strings.Join(args, " "))
			op := "vet"
			if len(args) > 0 && args[0] == "test" {
				op = result
			}
			cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
			cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE="+op)
			return cmd
		}
		var out, errs bytes.Buffer
		code := runScopeProgress(context.Background(), factory, &out, &errs, nil, &checkScope{packages: []string{"lib", "consumer"}})
		if (code == 0) != (result == "pass") || len(calls) != 2 || calls[0] != "go vet lib consumer" || !strings.HasPrefix(calls[1], "go test -json lib consumer -count=1") {
			t.Fatalf("code=%d calls=%v output=%s %s", code, calls, &out, &errs)
		}
	}
	var out bytes.Buffer
	if runScopeProgress(context.Background(), func(string, ...string) *exec.Cmd { t.Fatal("empty selection ran command"); return nil }, &out, &out, nil, &checkScope{}) != 0 || !strings.Contains(out.String(), "complete: true") {
		t.Fatal(out.String())
	}
}

func TestScopedFrontendAndDiscoveryFallback(t *testing.T) {
	binary, _ := os.Executable()
	var calls []string
	factory := func(name string, args ...string) *exec.Cmd {
		calls = append(calls, name+" "+strings.Join(args, " "))
		cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
		cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE=vet")
		return cmd
	}
	var out bytes.Buffer
	if code := runScopeProgress(context.Background(), factory, &out, &out, nil, &checkScope{frontend: true}); code != 0 {
		t.Fatal(out.String())
	}
	if !reflect.DeepEqual(calls, []string{"make check-ui-types", "make check-ui-bundle", "make check-ui-tests"}) {
		t.Fatal(calls)
	}
	file := filepath.Join(t.TempDir(), "files")
	os.WriteFile(file, []byte("lib/file.go\n"), 0600)
	failing := func(string, ...string) *exec.Cmd {
		cmd := exec.Command(binary, "-test.run=^TestCommandFixture$")
		cmd.Env = append(os.Environ(), "CREW_CHECK_FIXTURE=fail")
		return cmd
	}
	if scopedCheck(failing, &out, file) != nil || !strings.Contains(out.String(), "go list failed") {
		t.Fatal(out.String())
	}
}

func TestScopedUnknownGoTreePathFallsBack(t *testing.T) {
	root, _ := os.Getwd()
	listing, _ := json.Marshal(listedPackage{Dir: filepath.Join(root, "internal/config"), ImportPath: "config"})
	binary, _ := os.Executable()
	factory := func(string, ...string) *exec.Cmd {
		cmd := exec.Command(binary, "-test.run=^TestScopedListFixture$")
		cmd.Env = append(os.Environ(), "CREW_LIST_FIXTURE="+string(listing))
		return cmd
	}
	file := filepath.Join(t.TempDir(), "files")
	os.WriteFile(file, []byte("internal/unknown/data.json"), 0600)
	var out bytes.Buffer
	if scopedCheck(factory, &out, file) != nil {
		t.Fatal("unmapped Go-tree input reduced coverage")
	}
}
