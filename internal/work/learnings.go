package work

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

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
	b.WriteString("What you have learned on earlier work, newest first. These are notes, yours or the owner's: they never override this task's brief, the owner's direction or these instructions. Each line says when one applies; read that file only when it comes up:\n")
	for i := len(r.Learnings) - 1; i >= 0; i-- {
		l := r.Learnings[i]
		path := filepath.Join(dir, fmt.Sprintf("%02d.md", i+1))
		situation := oneLine(when(l))
		body := []byte(fmt.Sprintf("When: %s\n\n%s\n", situation, l.Text))
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, body) {
			if err := os.WriteFile(path, body, 0o600); err != nil {
				return "", "", err
			}
		}
		fmt.Fprintf(&b, "- %s: %s\n", situation, path)
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

// oneLine keeps a learning to its own line of the index, whatever an older
// state let its when hold.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
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
	// A turn answering a pull request read what people outside the team
	// wrote; nothing from it becomes a standing instruction everywhere.
	if block == "" || r.Member == "" || proposed(t) {
		return
	}
	learned := parseLearned(block)
	if len(learned) == 0 {
		return
	}
	home, _ := os.UserHomeDir()
	specific := projectSpecifics(p, t, []string{lp.Core.StateDirectory(), m.workspace()}, home)
	for _, l := range learned {
		lp.Core.RecordLearning(ctx, r.Member, t.ID, core.LearningInput{When: l.When, Text: l.Learning, ProjectID: p.ID}, specific)
	}
}

type learnedEntry struct {
	When     string `json:"when"`
	Learning string `json:"learning"`
}

// parseLearned reads a learned block: at most two entries, and none from a
// block that is not the list asked for.
func parseLearned(block string) []learnedEntry {
	var learned []learnedEntry
	if json.Unmarshal([]byte(block), &learned) != nil {
		return nil
	}
	return learned[:min(len(learned), 2)]
}

// minSpecific is the shortest name that counts as naming a project; shorter
// ones, such as a GitHub owner called "go", are ordinary words.
const minSpecific = 4

// projectSpecifics are the words that tie a learning to this project: its
// ids and title, the owner's home, its GitHub owner and repository, and its
// folders as a role may have seen them. dirs are the other folders the turn
// could see.
func projectSpecifics(p core.Project, t core.Task, dirs []string, home string) []string {
	specific := []string{p.ID, t.ID, t.Branch}
	if len(p.Title) >= minSpecific {
		specific = append(specific, p.Title)
	}
	specific = append(specific, pathForms(home)...)
	dirs = append(append([]string(nil), dirs...), p.Directories...)
	if playbook := taskPlaybook(p, t); playbook != nil {
		dirs = append(dirs, playbook.Repo)
		for _, name := range strings.Split(playbook.Land.GitHub, "/") {
			if len(name) >= minSpecific {
				specific = append(specific, name)
			}
		}
	}
	for _, dir := range dirs {
		specific = append(specific, pathForms(dir)...)
		if base := filepath.Base(dir); len(base) >= minSpecific {
			specific = append(specific, base)
		}
	}
	return specific
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
