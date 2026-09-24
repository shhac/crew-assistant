package work

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// learningsDir is where a task's copy of one member's learnings is kept for
// the role to read: under the state directory, never in the workspace, so
// nothing a role reads here can end up in a draft or a commit.
func (lp *Loop) learningsDir(t core.Task, r core.Role) string {
	return filepath.Join(lp.Core.StateDirectory(), "learnings", t.ID, r.Member)
}

// learningsIndex writes a role's pinned learnings as files and returns the
// index it starts with: when each one applies, and where to read it. Like a
// skill, a learning is read only when its situation comes up, so thirty of
// them cost the task a few lines rather than pages. Both come only from the
// pinned copy, so every turn of the task is told the same thing.
func (lp *Loop) learningsIndex(t core.Task, r core.Role) (string, string, error) {
	if len(r.Learnings) == 0 || r.Member == "" {
		return "", "", nil
	}
	dir := lp.learningsDir(t, r)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	var b strings.Builder
	b.WriteString("What you have learned on earlier work, newest first. Each line says when it applies; read that file only when it comes up, and follow it unless this task's brief says otherwise:\n")
	for i := len(r.Learnings) - 1; i >= 0; i-- {
		l := r.Learnings[i]
		path := filepath.Join(dir, fmt.Sprintf("%02d.md", i+1))
		body := []byte(fmt.Sprintf("When: %s\n\n%s\n", when(l), l.Text))
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, body) {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return "", "", err
			}
		}
		fmt.Fprintf(&b, "- %s: %s\n", when(l), path)
	}
	return dir, strings.TrimRight(b.String(), "\n"), nil
}

// when is a learning's situation, or its opening words when it was recorded
// without one.
func when(l core.Learning) string {
	if l.When != "" {
		return l.When
	}
	first, _, _ := strings.Cut(l.Text, "\n")
	if i := strings.Index(first, ". "); i > 0 {
		first = first[:i]
	}
	return text.Clip(first, 120)
}

// sweepLearnings removes the copies kept for tasks that have finished.
func (lp *Loop) sweepLearnings(snap core.Snapshot) {
	root := filepath.Join(lp.Core.StateDirectory(), "learnings")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if t, ok := findTask(snap, "", e.Name()); !ok || t.Finished() {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
