// Package bundledskills contains the daemon's small, versioned, prose-only catalog.
package bundledskills

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	harness "github.com/shhac/lib-agent-harness"
)

//go:embed */SKILL.md
var files embed.FS

type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role"`
}

var catalog = []Entry{
	{"sprite-atlas", "Generate and review consistent character animation strips for the production hand-off.", "designer"},
	{"sprite-atlas-pipeline", "Deterministically extract, validate and integrate designer-delivered animation strips with provenance.", "implementer"},
}

func Catalog() []Entry { return slices.Clone(catalog) }
func Known(name string) bool {
	return slices.ContainsFunc(catalog, func(e Entry) bool { return e.Name == name })
}

// Selection excludes non-applicable and disabled contents from the digest.
// The harness hashes names and permissions, but not file contents itself.
func Selection(role string, disabled []string) (entries []Entry, digest string) {
	h := sha256.New()
	for _, e := range catalog {
		if e.Role != role || slices.Contains(disabled, e.Name) {
			continue
		}
		data, err := files.ReadFile(e.Name + "/SKILL.md")
		if err != nil {
			panic(err)
		} // Embedded catalog is checked by tests.
		entries = append(entries, e)
		fmt.Fprintf(h, "%s\x00%d\x00", e.Name, len(data))
		h.Write(data)
	}
	if len(entries) > 0 {
		digest = fmt.Sprintf("%x", h.Sum(nil))
	}
	return
}

var publication sync.Mutex

// Prepare publishes immutable, content-addressed directories outside role write
// roots. Never remove them at turn cleanup: another session may still read them.
func Prepare(stateDir, role string, disabled []string) (harness.Skills, string, error) {
	entries, digest := Selection(role, disabled)
	set := harness.Skills{}
	if len(entries) == 0 {
		return set, "", nil
	}
	set.Delivery = harness.SkillDeliveryComposed
	publication.Lock()
	defer publication.Unlock()
	// Resolve the caller's state directory (including macOS /var aliases), then
	// refuse links inside our own storage rather than following them.
	base, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return set, "", err
	}
	root := filepath.Join(base, "bundled-skills")
	if err := ensureDirectory(root); err != nil {
		return set, "", err
	}
	for _, e := range entries {
		data, err := files.ReadFile(e.Name + "/SKILL.md")
		if err != nil {
			return set, "", err
		}
		dir, err := publish(root, e.Name, data, nil)
		if err != nil {
			return set, "", fmt.Errorf("bundled skill %s: %w", e.Name, err)
		}
		set.Provided = append(set.Provided, harness.Skill{Name: e.Name, Dir: dir, Scripts: false})
	}
	return set, digest, nil
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !safeDirectoryPermissions(info.Mode(), 0077, runtime.GOOS) {
		return fmt.Errorf("unsafe skill directory %s", path)
	}
	return nil
}

// beforePublish is an extraction-failure seam, used only by synthetic tests.
func publish(root, name string, data []byte, beforePublish func() error) (string, error) {
	if !Known(name) {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	if !bytes.HasPrefix(data, []byte("---\nname: "+name+"\n")) {
		return "", fmt.Errorf("invalid skill manifest")
	}
	sum := sha256.Sum256(data)
	dir := filepath.Join(root, fmt.Sprintf("%s-%x", name, sum))
	if _, err := os.Lstat(dir); err == nil {
		return dir, verify(dir, data)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	temp, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	if err := os.WriteFile(filepath.Join(temp, "SKILL.md"), data, 0400); err != nil {
		return "", err
	}
	if beforePublish != nil {
		if err := beforePublish(); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(temp, 0700); err != nil {
		return "", err
	}
	if err := os.Rename(temp, dir); err != nil {
		// Independent daemon publications can race. Only identical complete data
		// counts as success; corrupt or unsafe winners stop the launch.
		if check := verify(dir, data); check != nil {
			return "", err
		}
	}
	return dir, verify(dir, data)
}

func verify(dir string, expected []byte) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || !safeDirectoryPermissions(info.Mode(), 0022, runtime.GOOS) {
		return fmt.Errorf("unsafe published skill directory")
	}
	children, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(children) != 1 || children[0].Name() != "SKILL.md" {
		return fmt.Errorf("unexpected skill files")
	}
	path := filepath.Join(dir, "SKILL.md")
	info, err = os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 {
		return fmt.Errorf("unsafe skill file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expected) {
		return fmt.Errorf("corrupt bundled skill")
	}
	return nil
}

// Windows directory mode bits describe attributes, not POSIX access control.
// Publication stays beneath daemon state; role write access is restricted by
// the harness sandbox on every platform. File read-only attributes, directory
// types, symlinks and contents are still checked independently.
func safeDirectoryPermissions(mode os.FileMode, forbidden os.FileMode, platform string) bool {
	return platform == "windows" || mode.Perm()&forbidden == 0
}

func Fingerprint(digest string) string {
	if digest == "" {
		return ""
	}
	return "Bundled skill contents: " + digest
}
