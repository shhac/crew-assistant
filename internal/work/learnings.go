package work

import (
	"bytes"
	"context"
	"encoding/json"
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

// learnedGuide asks a member to keep what a turn taught it, on the rule the
// owner set: a shared learning is never about one project, and any data in
// it is made up. Roles not copied from a member have nowhere to keep it.
func learnedGuide(r core.Role, afterJSON bool) string {
	if r.Member == "" {
		return ""
	}
	where := "you may end your reply with"
	if afterJSON {
		where = "you may add, after the JSON object,"
	}
	return "\n\nIf this turn taught you something you would do again in every project, " + where + ":\n```learned\n" +
		`[{"when": "the situation it applies to, in a few words", "learning": "what to do, and why"}]` +
		"\n```\nAt most two. Never put anything from this project in one: no names, paths, repositories, people, code or data; if an example helps, make one up. Leave out anything your learnings already cover. Most turns teach nothing new; then add nothing.\n"
}

// recordLearned keeps what a member said it learned. A learning that breaks
// the rule is dropped rather than failing the turn; the work itself stands.
func (lp *Loop) recordLearned(ctx context.Context, p core.Project, t core.Task, r core.Role, m medium, block string) {
	if block == "" || r.Member == "" {
		return
	}
	var learned []struct {
		When     string `json:"when"`
		Learning string `json:"learning"`
	}
	if json.Unmarshal([]byte(block), &learned) != nil {
		return
	}
	var specific []string
	for _, dir := range append([]string{lp.Core.StateDirectory(), m.workspace()}, p.Directories...) {
		specific = append(specific, pathForms(dir)...)
	}
	if playbook := taskPlaybook(p, t); playbook != nil {
		specific = append(append(specific, pathForms(playbook.Repo)...), playbook.Land.GitHub)
	}
	for _, l := range learned[:min(len(learned), 2)] {
		lp.Core.RecordLearning(ctx, r.Member, t.ID, core.LearningInput{When: l.When, Text: l.Learning, ProjectID: p.ID}, specific)
	}
}

// pathForms is a folder as a role may have seen it: as given, with links
// resolved, and without macOS's /private prefix.
func pathForms(dir string) []string {
	if dir == "" {
		return nil
	}
	forms := []string{dir, strings.TrimPrefix(dir, "/private")}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		forms = append(forms, real, strings.TrimPrefix(real, "/private"))
	}
	return forms
}
