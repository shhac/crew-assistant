package localdocs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const (
	maxFiles     = 200
	maxTotalSize = 16 << 20
)

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
