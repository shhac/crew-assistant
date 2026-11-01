package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type checkScope struct {
	packages []string
	frontend bool
}

type listedPackage struct {
	Dir, ImportPath                    string
	Imports, TestImports, XTestImports []string
}

// A nil scope always means the full check. Unknown inputs fail open to full
// coverage, never to a smaller selection.
func scopedCheck(factory func(string, ...string) *exec.Cmd, out io.Writer, file string) *checkScope {
	full := func(reason string) *checkScope {
		fmt.Fprintln(out, "Scoped check falling back to full check:", reason)
		return nil
	}
	if file == "" {
		return full("no CREW_CHANGED_FILES record")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return full(err.Error())
	}
	files := strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
	if len(files) == 0 {
		fmt.Fprintln(out, "Scoped check: no changed files; nothing to check")
		return &checkScope{}
	}
	for i, name := range files {
		if filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name), "..") {
			return full("path outside repository")
		}
		name = filepath.ToSlash(filepath.Clean(name))
		files[i] = name
		if name == "go.mod" || name == "go.sum" || name == "Makefile" || strings.HasPrefix(name, "cmd/check-project/") || strings.HasPrefix(name, "internal/checktest/") {
			return full("check infrastructure changed: " + name)
		}
	}

	cmd := factory("go", "list", "-json", "./...")
	data, err = cmd.Output()
	if err != nil {
		return full("go list failed: " + err.Error())
	}
	var packages []listedPackage
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var p listedPackage
		err := decoder.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return full("invalid go list output: " + err.Error())
		}
		packages = append(packages, p)
	}
	root, err := os.Getwd()
	if err != nil {
		return full(err.Error())
	}
	root = scopeDirectory(root)
	byDir := map[string]string{}
	for _, p := range packages {
		byDir[scopeDirectory(p.Dir)] = p.ImportPath
	}
	selected := map[string]bool{}
	scope := &checkScope{}
	for _, name := range files {
		if strings.HasPrefix(name, "internal/dashboard/ui/") {
			scope.frontend = true
			continue
		}
		if strings.HasPrefix(name, "internal/dashboard/assets/") {
			scope.frontend = true
		}
		// The example config is read by internal/config tests.
		if name == "config.example.json" {
			name = "internal/config/config.go"
		}
		dir := scopeDirectory(filepath.Dir(filepath.Join(root, name)))
		for dir != root && byDir[dir] == "" {
			dir = filepath.Dir(dir)
		}
		pkg := byDir[dir]
		if pkg == "" {
			if documentationPath(name) {
				continue
			}
			return full("cannot map changed path to a Go package: " + name)
		}
		selected[pkg] = true
	}
	for changed := true; changed; {
		changed = false
		for _, p := range packages {
			if selected[p.ImportPath] {
				continue
			}
			imports := append(append(append([]string{}, p.Imports...), p.TestImports...), p.XTestImports...)
			for _, dep := range imports {
				if selected[dep] {
					selected[p.ImportPath] = true
					changed = true
					break
				}
			}
		}
	}
	for pkg := range selected {
		scope.packages = append(scope.packages, pkg)
	}
	if len(scope.packages) == 0 && !scope.frontend {
		fmt.Fprintln(out, "Scoped check: no affected packages; nothing to check")
	}
	sort.Strings(scope.packages)
	fmt.Fprintf(out, "Scoped check: packages=%v frontend=%t\n", scope.packages, scope.frontend)
	return scope
}

// Only known prose outside the Go tree is exempt; unknown inputs keep the
// full fallback. Files under package directories still select their package.
func documentationPath(name string) bool {
	if strings.HasSuffix(name, ".go") {
		return false
	}
	if strings.HasPrefix(name, "design-docs/") {
		return true
	}
	if strings.Contains(name, "/") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), ".md") || name == "LICENSE"
}
func scopeDirectory(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(dir)
}
