package gitrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/procgroup"
)

// Readable grants the module cache and named build toolchains to roles and
// hosted commands. Xcode, CommandLineTools and Homebrew are already in the
// harness system read set.
func (r Repo) Readable() []string {
	var read []string
	for _, dir := range []string{moduleCache(), goRoot(), nodeRoot()} {
		if dir != "" {
			read = append(read, dir)
		}
	}
	return read
}

// toolchainRoot resolves existing directories, refusing roots containing home or Crew state.
func toolchainRoot(dir string) string {
	if !filepath.IsAbs(dir) {
		return ""
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	rel, err := filepath.Rel(dir, home)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return ""
	}
	paths, err := config.Paths()
	if err != nil {
		return ""
	}
	for _, protected := range []string{filepath.Join(home, ".local"), filepath.Join(home, ".config"), filepath.Dir(paths.State), filepath.Dir(paths.Config)} {
		// Resolve the existing ancestor even when the config/state has not been created.
		var suffix []string
		for {
			if resolved, err := filepath.EvalSymlinks(protected); err == nil {
				protected = filepath.Join(append([]string{resolved}, suffix...)...)
				break
			}
			parent := filepath.Dir(protected)
			if parent == protected {
				return ""
			}
			suffix = append([]string{filepath.Base(protected)}, suffix...)
			protected = parent
		}
		rel, err := filepath.Rel(dir, protected)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return ""
		}
	}
	return dir
}

var goRoot = sync.OnceValue(func() string {
	cmd := exec.Command("go", "env", "GOROOT")
	procgroup.Detach(cmd)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return toolchainRoot(strings.TrimSpace(string(out)))
})

var nodeRoot = sync.OnceValue(func() string {
	path, err := exec.LookPath("node")
	if err != nil {
		return ""
	}
	return nodeInstallRoot(path)
})

func nodeInstallRoot(binary string) string {
	path, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return ""
	}
	root := toolchainRoot(filepath.Dir(filepath.Dir(path)))
	// Shared system prefixes are covered by the harness already. Never turn
	// a shared home prefix into a read grant just because it has a Node binary.
	if root == "" || root == "/usr/local" || root == "/usr" || root == "/opt/homebrew" {
		return ""
	}
	for _, marker := range []string{"include/node", "lib/node_modules/npm"} {
		if info, err := os.Stat(filepath.Join(root, marker)); err == nil && info.IsDir() {
			return root
		}
	}
	return ""
}

// moduleCache is the owner's Go module cache, asked once of the Go toolchain
// from outside any repository so no project setting can move it.
var moduleCache = sync.OnceValue(func() string {
	cmd := exec.Command("go", "env", "GOMODCACHE")
	procgroup.Detach(cmd)
	cmd.Dir = os.TempDir()
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || !filepath.IsAbs(dir) {
		return ""
	}
	return dir
})

// Env is the environment roles need to build and test inside their sandbox:
// caches and temporary files in the clone, and no attempts at the network.
func (r Repo) Env() []string {
	return WorkspaceEnv(r.Workspace())
}

// WorkspaceEnv keeps build caches inside a writable workspace, including
// disposable command-sandbox copies that cannot write to QA's scratch root.
func WorkspaceEnv(dir string) []string {
	return envAt(filepath.Join(dir, cacheDir))
}

// envAt is that environment with its caches and temporary files in cache,
// made if the folder it is in exists.
func envAt(cache string) []string {
	if _, err := os.Stat(filepath.Dir(cache)); err == nil {
		for _, dir := range []string{"go-build", "tmp", "npm", "xdg"} {
			_ = os.MkdirAll(filepath.Join(cache, dir), 0700)
		}
	}
	env := []string{
		"GOCACHE=" + filepath.Join(cache, "go-build"),
		"TMPDIR=" + filepath.Join(cache, "tmp"),
		"npm_config_cache=" + filepath.Join(cache, "npm"),
		"XDG_CACHE_HOME=" + filepath.Join(cache, "xdg"),
		"npm_config_update_notifier=false",
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"CI=1",
	}
	if dir := moduleCache(); dir != "" {
		env = append(env, "GOMODCACHE="+dir)
	}
	return env
}
