package gitrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shhac/crew-assistant/internal/procgroup"
)

// Readable is what roles may read outside the clone: the owner's Go module
// cache, so an offline build finds the modules the owner already has.
func (r Repo) Readable() []string {
	if dir := moduleCache(); dir != "" {
		return []string{dir}
	}
	return nil
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
	return envAt(filepath.Join(r.Workspace(), cacheDir))
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
