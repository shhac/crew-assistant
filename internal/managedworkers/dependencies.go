package managedworkers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/statepath"
	"github.com/shhac/crew-assistant/internal/workerbroker"
)

type dependencyPlan struct {
	Files map[string][]byte
	Go    []string
	NPM   []string
}
type dependencyState struct {
	Digest string   `json:"digest"`
	Go     bool     `json:"go"`
	NPM    []string `json:"npm"`
}

func dependencyManifests(workspace string) (dependencyPlan, error) {
	plan := dependencyPlan{Files: map[string][]byte{}}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return plan, err
	}
	defer root.Close()
	entries := 0
	total := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect project dependency manifests")
		}
		entries++
		if entries > 20000 {
			return errors.New("the project has too many paths to inspect automatically; use a smaller source folder")
		}
		if name == "." {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" && d.Name() != "package-lock.json" {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("dependency manifests must be regular files, not links")
		}
		names := []string{name}
		base := path.Dir(name)
		if d.Name() == "go.mod" {
			plan.Go = append(plan.Go, base)
			if _, e := root.Lstat(path.Join(base, "go.sum")); e == nil {
				names = append(names, path.Join(base, "go.sum"))
			}
		} else {
			plan.NPM = append(plan.NPM, base)
			names = append(names, path.Join(base, "package.json"))
		}
		if len(plan.Go)+len(plan.NPM) > 16 {
			return errors.New("automatic dependency setup supports up to 16 Go or npm packages per project")
		}
		for _, file := range names {
			info, e := root.Lstat(file)
			if e != nil || !info.Mode().IsRegular() || info.Size() > 8*1024*1024 {
				return errors.New("dependency manifest is missing, linked, or exceeds 8 MiB")
			}
			f, e := root.Open(file)
			if e != nil {
				return e
			}
			raw, e := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
			f.Close()
			if e != nil || len(raw) > 8*1024*1024 {
				return errors.New("dependency manifest exceeds its read limit")
			}
			total += len(raw)
			if total > 16*1024*1024 {
				return errors.New("dependency manifests exceed 16 MiB")
			}
			plan.Files[file] = raw
		}
		return nil
	})
	if err != nil {
		return plan, err
	}
	for _, dir := range plan.Go {
		if unsafeGoReplace.Match(plan.Files[path.Join(dir, "go.mod")]) {
			return plan, errors.New("this project uses local module replacements; provide public module dependencies or a prepared offline toolchain")
		}
	}
	for _, dir := range plan.NPM {
		if err = validateNPM(plan.Files[path.Join(dir, "package.json")], plan.Files[path.Join(dir, "package-lock.json")]); err != nil {
			return plan, err
		}
	}
	sort.Strings(plan.Go)
	sort.Strings(plan.NPM)
	return plan, nil
}

var unsafeGoReplace = regexp.MustCompile(`(?m)=>\s+(?:\.|/|[A-Za-z]:|~)`)

func validateNPM(pkg, lock []byte) error {
	var document map[string]any
	var packageJSON map[string]any
	if json.Unmarshal(lock, &document) != nil || json.Unmarshal(pkg, &packageJSON) != nil {
		return errors.New("project dependency manifests contain invalid JSON")
	}
	version, _ := document["lockfileVersion"].(float64)
	if version != 2 && version != 3 {
		return errors.New("automatic npm setup requires a version 2 or 3 package lock")
	}
	packages, ok := document["packages"].(map[string]any)
	if !ok || len(packages) > 10000 {
		return errors.New("npm package lock is missing or too large")
	}
	validateSpecs := func(v map[string]any) bool {
		for _, key := range []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"} {
			if deps, ok := v[key].(map[string]any); ok {
				for _, raw := range deps {
					spec, ok := raw.(string)
					if !ok {
						return false
					}
					if strings.ContainsAny(spec, "/\\") && !strings.HasPrefix(spec, "npm:") {
						return false
					}
					if strings.Contains(spec, ":") && !strings.HasPrefix(spec, "npm:") {
						return false
					}
				}
			}
		}
		return true
	}
	if !validateSpecs(packageJSON) {
		return errors.New("automatic npm setup supports public registry dependencies only; custom, local and Git dependencies need a prepared offline toolchain")
	}
	for name, raw := range packages {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("npm package lock has an invalid entry")
		}
		if linked, _ := entry["link"].(bool); linked {
			return errors.New("npm workspace links need a prepared offline toolchain")
		}
		if name != "" && (!strings.HasPrefix(name, "node_modules/") || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00")) {
			return errors.New("npm package lock has an unsafe package path")
		}
		if !validateSpecs(entry) {
			return errors.New("npm package lock includes non-registry dependencies; a prepared offline toolchain is required")
		}
		resolved, _ := entry["resolved"].(string)
		if resolved != "" {
			u, e := url.Parse(resolved)
			if e != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Host != "registry.npmjs.org" && u.Host != "registry.yarnpkg.com") {
				return errors.New("npm dependencies must resolve through the public npm registry; private credentials are never supplied to workers")
			}
		}
	}
	return nil
}
func dependencyDigest(plan dependencyPlan) string {
	names := make([]string, 0, len(plan.Files))
	for name := range plan.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%d:%s:%d:", len(name), name, len(plan.Files[name]))
		h.Write(plan.Files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}
func loadDependencies(dir string) ([]workerbroker.DependencyMount, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "dependencies.json"))
	if err != nil {
		return nil, errors.New("project dependencies are not prepared; ask the assistant to prepare the worker")
	}
	var state dependencyState
	if json.Unmarshal(raw, &state) != nil || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(state.Digest) {
		return nil, errors.New("saved dependency state is invalid")
	}
	cache, err := statepath.EnsureDirectory(dir, "dependencies", state.Digest)
	if err != nil {
		return nil, err
	}
	mounts := []workerbroker.DependencyMount{}
	if state.Go {
		mounts = append(mounts, workerbroker.DependencyMount{Source: filepath.Join(cache, "gomod"), Target: "/opt/agent-assistant/gomod"})
	}
	for _, name := range state.NPM {
		if name != "." && (path.Clean(name) != name || !filepath.IsLocal(name) || strings.ContainsAny(name, "\\,:\x00\r\n")) {
			return nil, errors.New("invalid dependency package location")
		}
		mounts = append(mounts, workerbroker.DependencyMount{Source: filepath.Join(cache, "npm", filepath.FromSlash(name), "node_modules"), Target: path.Join("/workspace", name, "node_modules")})
	}
	for _, mount := range mounts {
		info, e := os.Lstat(mount.Source)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("cached dependencies are missing; ask the assistant to prepare the worker again")
		}
	}
	return mounts, nil
}
func (r *localRuntime) prepareDependencies(ctx context.Context, root, dir, workspace string, env environment) ([]workerbroker.DependencyMount, error) {
	plan, err := dependencyManifests(workspace)
	if err != nil {
		return nil, err
	}
	digest := dependencyDigest(plan)
	raw, e := os.ReadFile(filepath.Join(dir, "dependencies.json"))
	var previous dependencyState
	if e == nil && json.Unmarshal(raw, &previous) == nil && previous.Digest == digest {
		if mounts, e := loadDependencies(dir); e == nil {
			return mounts, nil
		}
	}
	cache, err := statepath.EnsureDirectory(dir, "dependencies", digest)
	if err != nil {
		return nil, err
	}
	// Only manifests enter the online preparation container. Project source and
	// package scripts cannot execute; the eventual worker has no network at all.
	input, err := os.MkdirTemp(cache, "manifests-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(input)
	for name, raw := range plan.Files {
		target := filepath.Join(input, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return nil, err
		}
		if err = os.WriteFile(target, raw, 0644); err != nil {
			return nil, err
		}
	}
	if err = os.MkdirAll(filepath.Join(cache, "gomod"), 0755); err != nil {
		return nil, err
	}
	for _, dir := range plan.NPM {
		folder := filepath.Join(cache, "npm", filepath.FromSlash(dir))
		if err = os.MkdirAll(folder, 0755); err != nil {
			return nil, err
		}
		for _, name := range []string{"package.json", "package-lock.json"} {
			if err = os.WriteFile(filepath.Join(folder, name), plan.Files[path.Join(dir, name)], 0644); err != nil {
				return nil, err
			}
		}
	}
	for _, dir := range plan.Go {
		folder := filepath.Join(cache, "go", filepath.FromSlash(dir))
		if err = os.MkdirAll(folder, 0755); err != nil {
			return nil, err
		}
		for _, name := range []string{"go.mod", "go.sum"} {
			if raw, ok := plan.Files[path.Join(dir, name)]; ok {
				if err = os.WriteFile(filepath.Join(folder, name), raw, 0644); err != nil {
					return nil, err
				}
			}
		}
		// Go only reads the mounted manifests; every download goes through the public
		// checksum-verifying proxy, with direct/Git/private access disabled.
		args := []string{"/usr/local/go/bin/go", "mod", "download"}
		if err = r.download(ctx, root, input, cache, env, path.Join("/deps/go", dir), args); err != nil {
			return nil, errors.New("public Go dependencies could not be prepared; check connectivity and whether this project requires private or local modules")
		}
	}
	for _, dir := range plan.NPM {
		args := []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--registry=https://registry.npmjs.org", "--cache=/tmp/npm"}
		if err = r.download(ctx, root, input, cache, env, path.Join("/deps/npm", dir), args); err != nil {
			return nil, errors.New("public npm dependencies could not be prepared; check connectivity and lockfile compatibility; package scripts and private credentials are disabled")
		}
	}
	state := dependencyState{Digest: digest, Go: len(plan.Go) > 0, NPM: plan.NPM}
	raw, _ = json.Marshal(state)
	f, err := os.CreateTemp(dir, ".dependencies-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "dependencies.json")); err != nil {
		return nil, err
	}
	return loadDependencies(dir)
}
func (r *localRuntime) download(ctx context.Context, root, input, cache string, env environment, workdir string, command []string) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := "agent-assistant-setup-" + hex.EncodeToString(nonce[:])
	args := []string{"run", "--rm", "--name", name, "--label", "agent-assistant.setup=" + name, "--pull=never", "--network", "bridge", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--pids-limit", "128", "--memory", "2g", "--cpus", "2", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--tmpfs", "/tmp:rw,nosuid,nodev,size=1g", "--mount", "type=bind,src=" + input + ",dst=/manifests,readonly", "--mount", "type=bind,src=" + cache + ",dst=/deps", "--workdir", workdir}
	for _, value := range []string{"HOME=/tmp", "GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none", "GOTOOLCHAIN=local", "GOWORK=off", "GOMODCACHE=/deps/gomod", "GOCACHE=/tmp/gocache", "GOPATH=/tmp/go", "NPM_CONFIG_USERCONFIG=/dev/null", "NPM_CONFIG_GLOBALCONFIG=/tmp/agent-assistant-global-npmrc"} {
		args = append(args, "--env", value)
	}
	args = append(args, "--entrypoint", "/usr/bin/timeout", env.Image, "--kill-after=10s", "600")
	args = append(args, command...)
	_, runErr := r.docker(ctx, root, env.Socket, args, nil)
	cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, cleanErr := r.docker(cleanup, root, env.Socket, []string{"rm", "--force", name}, nil)
	if cleanErr != nil {
		names, listErr := r.docker(cleanup, root, env.Socket, []string{"container", "ls", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.Names}}"}, nil)
		if listErr != nil || strings.TrimSpace(string(names)) != "" {
			return errors.New("dependency preparation cleanup could not be confirmed; restart the local isolation service before retrying")
		}
	}
	return runErr
}

// An already-running broker owns immutable cache paths. Refreshing those beneath
// active runs would race readers, so explicit preparation reports the restart
// needed to safely replace them instead of claiming stale dependencies are ready.
func dependenciesCurrent(dir, workspace string) error {
	plan, err := dependencyManifests(workspace)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dependencies.json"))
	var state dependencyState
	if err != nil || json.Unmarshal(raw, &state) != nil || state.Digest != dependencyDigest(plan) {
		return errors.New("project dependencies changed; restart the assistant, then ask it to prepare this worker again; existing work is preserved")
	}
	if _, err = loadDependencies(dir); err != nil {
		return errors.New("this worker's cached dependencies are missing; restart the assistant, then ask it to prepare this worker again; existing work is preserved")
	}
	return nil
}
