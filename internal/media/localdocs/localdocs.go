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
	d := Docs{root: projectDir}
	if err := os.MkdirAll(d.Workspace(), 0700); err != nil {
		return Docs{}, err
	}
	return d, nil
}

// Workspace is where the writer works.
func (d Docs) Workspace() string { return filepath.Join(d.root, "workspace") }

func (d Docs) revision(taskID string, n int) string {
	return filepath.Join(d.root, "tasks", filepath.Base(taskID), "r"+strconv.Itoa(n))
}

// Snapshot records the workspace as revision n and returns its files.
func (d Docs) Snapshot(taskID string, n int) ([]string, error) {
	target := d.revision(taskID, n)
	if err := os.RemoveAll(target); err != nil {
		return nil, err
	}
	files, err := copyTree(d.Workspace(), target)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("the workspace has no files to record")
	}
	return files, nil
}

// Reset puts the workspace back to revision n, or empties it for n == 0, so
// whatever an interrupted or failed turn left behind is dropped.
func (d Docs) Reset(taskID string, n int) error {
	if err := os.RemoveAll(d.Workspace()); err != nil {
		return err
	}
	if err := os.MkdirAll(d.Workspace(), 0700); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err := copyTree(d.revision(taskID, n), d.Workspace())
	return err
}

// ReviewCopy gives a reviewer its own copy of revision n. Whatever the reviewer
// does to it cannot change the revision.
func (d Docs) ReviewCopy(taskID string, n int) (string, func(), error) {
	parent := filepath.Join(d.root, "reviews")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(parent, "r"+strconv.Itoa(n)+"-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if _, err = copyTree(d.revision(taskID, n), dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// File is one file of a revision as shown to the owner.
type File struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int64  `json:"size"`
}

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
