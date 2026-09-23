package workerbroker

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/statepath"
)

// npm tools commonly write transient data inside node_modules (for example
// Vite's config bundle). Each run gets its own bounded copy; the downloaded
// cache stays immutable and neither project sources nor host node_modules change.
func (b *Broker) prepareRunDependencies(ctx context.Context, workspace string) error {
	var total int64
	count := 0
	for _, mount := range b.cfg.Dependencies {
		if mount.Target == "/opt/agent-assistant/gomod" {
			continue
		}
		relative := strings.TrimPrefix(mount.Target, "/workspace/")
		if !filepath.IsLocal(relative) {
			return errors.New("invalid npm dependency location")
		}
		parent := workspace
		var err error
		if path.Dir(relative) != "." {
			parent, err = statepath.EnsureDirectory(workspace, strings.Split(path.Dir(relative), "/")...)
		}
		if err != nil {
			return err
		}
		staging, err := os.MkdirTemp(parent, ".agent-assistant-dependencies-")
		if err != nil {
			return err
		}
		err = copyDependencyTree(ctx, mount.Source, staging, &total, &count)
		if err != nil {
			os.RemoveAll(staging)
			return err
		}
		destination := filepath.Join(parent, "node_modules")
		// RemoveAll does not follow the directory entry if a previous interrupted
		// attempt left a symlink. The parent was anchored and validated above.
		if err = os.RemoveAll(destination); err == nil {
			err = os.Rename(staging, destination)
		}
		if err != nil {
			os.RemoveAll(staging)
			return err
		}
	}
	return nil
}
func copyDependencyTree(ctx context.Context, source, destination string, total *int64, count *int) error {
	root, err := os.OpenRoot(source)
	if err != nil {
		return err
	}
	defer root.Close()
	dest, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer dest.Close()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return errors.New("cannot copy prepared npm dependencies")
		}
		if name == "." {
			return nil
		}
		*count++
		if *count > 150000 {
			return errors.New("prepared npm dependencies exceed 150000 files")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := root.Readlink(name)
			if err != nil {
				return err
			}
			resolved := path.Clean(path.Join(path.Dir(name), target))
			if path.IsAbs(target) || !filepath.IsLocal(resolved) || strings.ContainsAny(target, "\\\x00") {
				return errors.New("prepared npm package contains a link outside its dependency tree")
			}
			return dest.Symlink(target, name)
		}
		if entry.IsDir() {
			return dest.Mkdir(name, 0755)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("prepared npm package contains an unsupported file")
		}
		if info.Size() > 64*1024*1024 {
			return errors.New("a prepared npm dependency exceeds 64 MiB")
		}
		*total += info.Size()
		if *total > 1024*1024*1024 {
			return errors.New("prepared npm dependencies exceed 1 GiB per run")
		}
		in, err := root.OpenFile(name, os.O_RDONLY|noFollowFlag, 0)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0644)
		if info.Mode()&0111 != 0 {
			mode = 0755
		}
		out, err := dest.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		written, err := io.Copy(out, io.LimitReader(in, 64*1024*1024+1))
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if written > 64*1024*1024 {
			return errors.New("dependency changed while copying")
		}
		return closeErr
	})
}
