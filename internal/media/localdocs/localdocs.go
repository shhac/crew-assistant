// Package localdocs is the medium for written work kept as files: a private
// workspace the writer edits, numbered revisions, disposable copies for
// reviewers, and a delivery that never overwrites anything already there.
package localdocs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shhac/crew-assistant/internal/media"
)

const (
	maxFiles     = 200
	maxTotalSize = 16 << 20
)

// Docs is one project's documents, under the project's private directory.
type Docs struct{ root string }

func Open(projectDir string) (Docs, error) {
	if !filepath.IsAbs(projectDir) {
		return Docs{}, errors.New("project directory must be absolute")
	}
	return Docs{root: projectDir}, nil
}

// Workspace is where the writer works on a task: the task's own, which no
// other task shares.
func (d Docs) Workspace(taskID string) string {
	return filepath.Join(d.task(taskID), "workspace")
}

func (d Docs) task(taskID string) string {
	return filepath.Join(d.root, "tasks", filepath.Base(taskID))
}

func (d Docs) revision(taskID string, n int) string {
	return filepath.Join(d.task(taskID), "r"+strconv.Itoa(n))
}

// Snapshot records the task's workspace as revision n and returns its files
// and the digest that identifies them.
func (d Docs) Snapshot(taskID string, n int) ([]string, string, error) {
	target := d.revision(taskID, n)
	if err := os.RemoveAll(target); err != nil {
		return nil, "", err
	}
	files, err := copyTree(d.Workspace(taskID), target)
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", errors.New("the workspace has no files to record")
	}
	sum, err := digest(target)
	return files, sum, err
}

// Digest identifies revision n's files and contents, or is "" when there is
// no revision n.
func (d Docs) Digest(taskID string, n int) (string, error) {
	dir := d.revision(taskID, n)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return digest(dir)
}

// Reset puts the task's workspace back to revision n, or empties it for
// n == 0, so whatever an interrupted or failed turn left behind is dropped.
func (d Docs) Reset(taskID string, n int) error {
	workspace := d.Workspace(taskID)
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err := copyTree(d.revision(taskID, n), workspace)
	return err
}

// RemoveWorkspace deletes a finished task's workspace. Its revisions stay.
func (d Docs) RemoveWorkspace(taskID string) error {
	return os.RemoveAll(d.Workspace(taskID))
}

// Workspaces names the tasks that have a workspace.
func (d Docs) Workspaces() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.root, "tasks"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var ids []string
	for _, e := range entries {
		if _, statErr := os.Stat(d.Workspace(e.Name())); statErr == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, err
}

// RemoveChecks deletes every copy a check left behind.
func (d Docs) RemoveChecks() error {
	return removeAll(filepath.Join(d.root, "reviews"))
}

// Check is one check's own copy of a revision: never the writer's
// workspace. Its files are read-only, and what the check writes goes to
// Scratch.
type Check struct {
	// Dir is the revision, copied read-only.
	Dir string
	// Scratch is where the check may write.
	Scratch string
	digest  string
	root    string
}

// ErrCopyChanged is a check that changed the revision it was checking.
var ErrCopyChanged = errors.New("the check changed the revision it was checking")

// Checkout copies revision n for one check, read-only, in a folder of its
// own beside a scratch folder. want is the digest the revision was recorded
// with: a copy that is not exactly it is refused.
func (d Docs) Checkout(taskID string, n int, want string) (Check, error) {
	parent := filepath.Join(d.root, "reviews")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Check{}, err
	}
	root, err := os.MkdirTemp(parent, "r"+strconv.Itoa(n)+"-")
	if err != nil {
		return Check{}, err
	}
	c := Check{Dir: filepath.Join(root, "copy"), Scratch: filepath.Join(root, "scratch"), digest: want, root: root}
	if err = c.fill(d.revision(taskID, n)); err != nil {
		c.Remove()
		return Check{}, err
	}
	return c, nil
}

func (c Check) fill(revision string) error {
	if _, err := copyTree(revision, c.Dir); err != nil {
		return err
	}
	if err := os.MkdirAll(c.Scratch, 0700); err != nil {
		return err
	}
	if err := c.Verify(); err != nil {
		return fmt.Errorf("the revision is not as recorded: %w", err)
	}
	return setWritable(c.Dir, false)
}

// Verify says the copy is still exactly the revision: the same files with
// the same contents, and nothing else, links included.
func (c Check) Verify() error {
	var extra []string
	err := filepath.WalkDir(c.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && !entry.Type().IsRegular() {
			extra = append(extra, path)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCopyChanged, err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("%w: %s", ErrCopyChanged, strings.Join(extra, ", "))
	}
	if got, err := digest(c.Dir); err != nil || got != c.digest {
		return fmt.Errorf("%w: its files differ", errors.Join(ErrCopyChanged, err))
	}
	return nil
}

// Remove deletes the copy and its scratch folder.
func (c Check) Remove() {
	if c.root != "" {
		_ = removeAll(c.root)
	}
}

func removeAll(dir string) error {
	_ = setWritable(dir, true)
	return os.RemoveAll(dir)
}

// setWritable takes write permission from, or gives it back to, every file
// and folder under dir. Links are left alone: changing one would change
// what it points at.
func setWritable(dir string, writable bool) error {
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() &^ 0o222
		if writable {
			mode |= 0o200
		}
		return os.Chmod(path, mode)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// File is one file of a revision as shown to the owner.
type File = media.File

// Preview returns revision n's files, each cut at limit bytes.
func (d Docs) Preview(taskID string, n int, limit int) ([]File, error) {
	root := d.revision(taskID, n)
	out := []File{}
	err := walkFiles(root, func(rel string, info fs.FileInfo) error {
		file := File{Path: rel, Size: info.Size()}
		raw, err := readLimited(filepath.Join(root, rel), limit)
		if err != nil {
			return err
		}
		file.Truncated = info.Size() > int64(len(raw))
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			file.Binary = true
		} else {
			file.Content = string(raw)
		}
		out = append(out, file)
		return nil
	})
	return out, err
}

// Deliver copies revision n into its own folder under dest and returns it.
// Nothing already in dest is overwritten. A folder left by an earlier attempt
// that matches the revision exactly counts as delivered, so a retried
// delivery settles rather than repeats.
func (d Docs) Deliver(taskID string, n int, dest, label string) (string, error) {
	if !filepath.IsAbs(dest) {
		return "", errors.New("delivery folder must be absolute")
	}
	info, err := os.Stat(dest)
	if err != nil || !info.IsDir() {
		return "", errors.New("delivery folder does not exist")
	}
	source := d.revision(taskID, n)
	want, err := digest(source)
	if err != nil {
		return "", err
	}
	base := slug(label) + "-r" + strconv.Itoa(n)
	for attempt := 0; attempt < 100; attempt++ {
		name := base
		if attempt > 0 {
			name = base + "-" + strconv.Itoa(attempt+1)
		}
		target := filepath.Join(dest, name)
		if _, statErr := os.Lstat(target); statErr == nil {
			if got, digestErr := digest(target); digestErr == nil && got == want {
				return target, nil
			}
			continue
		}
		if _, err = copyTree(source, target); err != nil {
			return "", err
		}
		return target, nil
	}
	return "", errors.New("no free delivery folder name")
}

func slug(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "draft"
	}
	return out
}

// walkFiles visits regular files under root in a stable order. Links and
// special files are skipped rather than followed.
func walkFiles(root string, visit func(rel string, info fs.FileInfo) error) error {
	var files []string
	infos := map[string]fs.FileInfo{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		infos[rel] = info
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, rel := range files {
		if err = visit(filepath.ToSlash(rel), infos[rel]); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(from, to string) ([]string, error) {
	var files []string
	var total int64
	err := walkFiles(from, func(rel string, info fs.FileInfo) error {
		if len(files) >= maxFiles {
			return fmt.Errorf("more than %d files", maxFiles)
		}
		if total += info.Size(); total > maxTotalSize {
			return errors.New("files are larger than a draft can be")
		}
		target := filepath.Join(to, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(from, filepath.FromSlash(rel)), target); err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return files, nil
	}
	return files, err
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	return target.Close()
}

func readLimited(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(limit)))
}

// digest identifies a folder's files and contents.
func digest(root string) (string, error) {
	hash := sha256.New()
	err := walkFiles(root, func(rel string, info fs.FileInfo) error {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintf(hash, "%s\x00%d\x00", rel, info.Size())
		_, err = io.Copy(hash, f)
		return err
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}
