package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/releaseversion"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

// SkillsManifestURL lists each skill the family publishes, with the version
// and time it was last synced.
const SkillsManifestURL = "https://raw.githubusercontent.com/shhac/agent-skills/HEAD/manifest.json"

const skillsSource = "shhac/agent-skills"

const failedRetry = 5 * time.Minute

// remoteBound keeps an unreachable GitHub from holding up the list.
const remoteBound = 10 * time.Second

// remote is what was last read from GitHub: each formula's version and the
// skills manifest. A tool or skill missing from it couldn't be read.
type remote struct {
	checked  time.Time
	versions map[string]string
	skills   map[string]manifestEntry
}

// fresh keeps a good read for LatestTTL, but retries one that found nothing
// sooner, so a dashboard opened offline recovers once the network is back.
func (r remote) fresh(now time.Time) bool {
	if r.checked.IsZero() {
		return false
	}
	ttl := LatestTTL
	if len(r.versions) == 0 || len(r.skills) == 0 {
		ttl = failedRetry
	}
	return now.Sub(r.checked) < ttl
}

type manifestEntry struct {
	Version  string    `json:"version"`
	SyncedAt time.Time `json:"synced_at"`
}

func (m *Manager) ensureRemote(ctx context.Context, force bool) {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	m.mu.Lock()
	fresh := m.remote.fresh(m.opts.Now())
	m.mu.Unlock()
	if fresh && !force {
		return
	}
	next := m.readRemote(ctx)
	// A caller that went away read nothing it can vouch for.
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	m.remote = next
	m.mu.Unlock()
}

// readRemote reads every formula and the skills manifest at once; whatever
// fails is left out.
func (m *Manager) readRemote(ctx context.Context) remote {
	ctx, cancel := context.WithTimeout(ctx, remoteBound)
	defer cancel()
	next := remote{checked: m.opts.Now(), versions: map[string]string{}, skills: map[string]manifestEntry{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tool := range catalog {
		wg.Go(func() {
			body, err := m.opts.Fetch(ctx, (config.UpgradeSettings{Formula: tool.Formula}).FormulaSourceURL())
			if err != nil {
				return
			}
			version, err := upgrade.FormulaVersion(body)
			if err != nil {
				return
			}
			mu.Lock()
			next.versions[tool.ID] = version
			mu.Unlock()
		})
	}
	wg.Go(func() {
		body, err := m.opts.Fetch(ctx, SkillsManifestURL)
		if err != nil {
			return
		}
		var manifest map[string]manifestEntry
		if json.Unmarshal(body, &manifest) == nil {
			next.skills = manifest
		}
	})
	wg.Wait()
	return next
}

// homebrewPrefix follows the daemon's own Homebrew install first, then
// whichever brew is reachable, then Homebrew's standard places.
func (m *Manager) homebrewPrefix() string {
	var candidates []string
	if exe, err := m.opts.Executable(); err == nil {
		if prefix, err := upgrade.HomebrewPrefix(exe); err == nil {
			candidates = append(candidates, prefix)
		}
	}
	if brew, err := m.opts.LookPath("brew"); err == nil {
		candidates = append(candidates, filepath.Dir(filepath.Dir(brew)))
	}
	for _, prefix := range append(candidates, m.opts.Prefixes...) {
		if executable(brewPath(prefix)) {
			return prefix
		}
	}
	return ""
}

func (m *Manager) binary(name, prefix string) string {
	if prefix != "" {
		if path := filepath.Join(prefix, "bin", name); executable(path) {
			return path
		}
	}
	if path, err := m.opts.LookPath(name); err == nil {
		return path
	}
	return ""
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// Locate finds one of the family's tools where the dashboard finds it,
// Homebrew's copy first and then PATH, and reads its version; either is ""
// when it can't.
func Locate(ctx context.Context, name string) (path, version string) {
	m := New(Options{})
	return m.installedVersion(ctx, name, m.homebrewPrefix())
}

// installedVersion finds a tool and reads its version; either is "" when it
// can't.
func (m *Manager) installedVersion(ctx context.Context, name, prefix string) (path, version string) {
	path = m.binary(name, prefix)
	if path == "" {
		return "", ""
	}
	return path, m.probeVersion(ctx, path)
}

// probeVersion reads `<tool> --version`, such as "lin version 0.36.4", and
// returns "" when it can't.
func (m *Manager) probeVersion(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, m.opts.ProbeTimeout)
	defer cancel()
	var out clipped
	if err := m.opts.Run(ctx, Command{Path: path, Args: []string{"--version"}, Env: m.toolEnv(), Output: &out}); err != nil {
		return ""
	}
	return parseVersion(string(out.data))
}

func parseVersion(output string) string {
	for _, word := range strings.Fields(output) {
		if version := versionLabel(strings.TrimRight(word, ",;)")); version != "" {
			return version
		}
	}
	return ""
}

func versionStatus(installed, latest string) string {
	switch {
	case installed == "":
		return StatusUnknown
	case latest != "" && releaseversion.Compare(installed, latest) < 0:
		return StatusOutdated
	}
	return StatusInstalled
}

// clipped keeps the start of a probe's output without blocking a chatty one.
type clipped struct{ data []byte }

func (c *clipped) Write(p []byte) (int, error) {
	if room := 4096 - len(c.data); room > 0 {
		c.data = append(c.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// Every process the toolkit starts gets only what it needs, and none of the
// daemon's own settings or credentials.
var (
	userKeys   = []string{"HOME", "USER", "LOGNAME", "TMPDIR"}
	xdgKeys    = []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"}
	systemPath = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
)

// toolEnv is what a tool needs to find its own config and credentials.
func (m *Manager) toolEnv() []string {
	return m.environment(slices.Concat([]string{"PATH"}, userKeys, xdgKeys)...)
}

func (m *Manager) brewEnv(prefix string) []string {
	path := slices.Concat([]string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "sbin")}, systemPath)
	return append(m.environment(slices.Concat(userKeys, []string{"LANG", "SHELL"})...),
		"PATH="+strings.Join(path, ":"), "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_COLOR=1")
}

// skillEnv puts npx's own node first on the PATH.
func (m *Manager) skillEnv(npx string) []string {
	path := slices.Concat([]string{filepath.Dir(npx)}, systemPath)
	return append(m.environment(slices.Concat(userKeys, []string{"LANG"}, xdgKeys)...), "PATH="+strings.Join(path, ":"))
}

func (m *Manager) environment(keys ...string) []string {
	env := []string{}
	for _, key := range keys {
		if v, ok := m.opts.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// skillState is the owner's installed skills, from the skills CLI's lock
// file. ok is false when the lock exists but can't be read.
type skillState struct {
	ok      bool
	updated map[string]time.Time
}

func (m *Manager) readSkills() skillState {
	state := skillState{ok: m.opts.Home != "", updated: map[string]time.Time{}}
	if !state.ok {
		return state
	}
	data, err := os.ReadFile(filepath.Join(m.opts.Home, ".agents", ".skill-lock.json"))
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	var lock struct {
		Skills map[string]struct {
			Source    string    `json:"source"`
			UpdatedAt time.Time `json:"updatedAt"`
		} `json:"skills"`
	}
	if err != nil || json.Unmarshal(data, &lock) != nil {
		state.ok = false
		return state
	}
	for name, entry := range lock.Skills {
		if entry.Source == skillsSource && !entry.UpdatedAt.IsZero() {
			state.updated[name] = entry.UpdatedAt
		}
	}
	return state
}

func (m *Manager) skillRow(name string, remote remote, skills skillState) SkillRow {
	row := SkillRow{Name: name, Command: skillCommand(name), Latest: versionLabel(remote.skills[name].Version)}
	switch {
	case m.opts.Home == "":
		row.Status = StatusUnknown
	case !m.skillPresent(name):
		row.Status = StatusMissing
	case !skills.ok:
		row.Status = StatusUnknown
	default:
		row.Status = StatusInstalled
		updated, tracked := skills.updated[name]
		if tracked {
			row.Installed = updated.UTC().Format(time.DateOnly)
		}
		// The lock records when a skill was last written, not its version,
		// so a newer sync upstream is what makes it out of date.
		if synced := remote.skills[name].SyncedAt; tracked && !synced.IsZero() && updated.Before(synced) {
			row.Status = StatusOutdated
		}
	}
	return row
}

func (m *Manager) skillPresent(name string) bool {
	for _, dir := range []string{filepath.Join(m.opts.Home, ".claude", "skills"), filepath.Join(m.opts.Home, ".agents", "skills")} {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

func versionLabel(v string) string {
	if !releaseversion.Valid(v) {
		return ""
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func skillCommand(name string) string {
	return "npx skills add " + skillsSource + " --skill " + name + " --global"
}

// npx comes from PATH, Homebrew's node, or the newest nvm install: a launchd
// daemon's PATH rarely includes nvm's.
func (m *Manager) npx(prefix string) string {
	if path, err := m.opts.LookPath("npx"); err == nil {
		return path
	}
	if prefix != "" {
		if path := filepath.Join(prefix, "bin", "npx"); executable(path) {
			return path
		}
	}
	if m.opts.Home == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(m.opts.Home, ".nvm", "versions", "node", "*", "bin", "npx"))
	best, bestVersion := "", ""
	for _, path := range matches {
		version := filepath.Base(filepath.Dir(filepath.Dir(path)))
		if !releaseversion.Valid(version) || !executable(path) {
			continue
		}
		if bestVersion == "" || releaseversion.Compare(version, bestVersion) > 0 {
			best, bestVersion = path, version
		}
	}
	return best
}

func commandLine(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = arg
		if arg == "" || strings.ContainsAny(arg, " \t\"'$`\\*?") {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
