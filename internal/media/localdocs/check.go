package localdocs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/media"
)

// RemoveChecks deletes every copy a check left behind.
func (d Docs) RemoveChecks() error {
	return media.RemoveReadOnly(filepath.Join(d.root, "reviews"))
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
	return media.SetWritable(c.Dir, false)
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
		return fmt.Errorf("%w: %w", media.ErrCheckChanged, err)
	}
	if len(extra) > 0 {
		return fmt.Errorf("%w: %s", media.ErrCheckChanged, strings.Join(extra, ", "))
	}
	if got, err := digest(c.Dir); err != nil || got != c.digest {
		return fmt.Errorf("%w: its files differ", errors.Join(media.ErrCheckChanged, err))
	}
	return nil
}

// Remove deletes the copy and its scratch folder.
func (c Check) Remove() {
	if c.root != "" {
		_ = media.RemoveReadOnly(c.root)
	}
}
