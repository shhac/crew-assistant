// Package localdocs is the medium for written work kept as files: a private
// workspace the writer edits, numbered revisions, disposable copies for
// reviewers, and a delivery that never overwrites anything already there.
package localdocs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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
