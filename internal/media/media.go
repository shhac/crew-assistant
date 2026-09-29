// Package media holds what every medium shares: what it shows the owner, and
// the read-only copies its checks run against.
package media

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// File is one file of a revision as shown to the owner.
type File struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int64  `json:"size"`
}

// ErrCheckChanged is a check that changed the revision it was given.
var ErrCheckChanged = errors.New("the check changed the revision it was checking")

// RemoveReadOnly deletes dir, giving write permission back first so what a
// check was given read-only can go.
func RemoveReadOnly(dir string) error {
	_ = SetWritable(dir, true)
	return os.RemoveAll(dir)
}

// SetWritable takes write permission from, or gives it back to, every file
// and folder under dir. Links are left alone: changing one would change
// what it points at.
func SetWritable(dir string, writable bool) error {
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
