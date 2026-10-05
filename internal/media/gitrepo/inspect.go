package gitrepo

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/media"
)

// Preview shows the owner what a revision changes, cut at limit bytes.
func (r Repo) Preview(ctx context.Context, base, commit string, limit int) ([]media.File, error) {
	stat, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--summary", base+".."+commit)
	if err != nil {
		return nil, err
	}
	patch, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-color", base+".."+commit)
	if err != nil {
		return nil, err
	}
	diff := media.File{Path: "changes.diff", Content: patch, Size: int64(len(patch))}
	if len(patch) > limit {
		diff.Content, diff.Truncated = patch[:limit], true
	}
	files := []media.File{{Path: "summary", Content: stat, Size: int64(len(stat))}, diff}
	if attention, err := r.Attention(ctx, base, commit); err == nil && len(attention) > 0 {
		note := "Look at these before running anything on this branch:\n- " + strings.Join(attention, "\n- ") + "\n"
		files = append([]media.File{{Path: "attention", Content: note, Size: int64(len(note))}}, files...)
	}
	return files, nil
}

// sensitive names files that run, or instruct agents, when someone next works
// in the repository: build and package scripts, hooks and editor settings,
// agent instructions, git attributes and submodules.
var sensitive = []string{"Makefile", "makefile", "GNUmakefile", "package.json", ".envrc", ".gitattributes", ".gitmodules", "AGENTS.md", "CLAUDE.md", "CLAUDE.local.md", "go.mod", "Dockerfile", ".npmrc"}

var sensitiveDirs = []string{".claude/", ".codex/", ".agents/", ".husky/", ".vscode/", ".github/", ".githooks/", ".devcontainer/"}

// Attention lists what a change touches that deserves a look before the owner
// runs anything on the delivered branch: sensitive files, and any change that
// adds a symlink or makes a file executable.
func (r Repo) Attention(ctx context.Context, base, commit string) ([]string, error) {
	return r.attention(ctx, base, commit, nil)
}

// OwnAttention is Attention for an update from pushed to commit, counting
// only the task's own change, what commit holds beyond its base: whatever
// a catch-up took in from the target was the target's already.
func (r Repo) OwnAttention(ctx context.Context, pushed, commit, base string) ([]string, error) {
	if base == "" {
		return r.Attention(ctx, pushed, commit)
	}
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", base+".."+commit)
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, path := range strings.Split(strings.TrimSpace(out), "\n") {
		own[path] = true
	}
	return r.attention(ctx, pushed, commit, own)
}

func (r Repo) attention(ctx context.Context, base, commit string, only map[string]bool) ([]string, error) {
	out, err := run(ctx, r.Workspace(), "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--raw", base+".."+commit)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		newMode, path := fields[1], fields[len(fields)-1]
		if only != nil && !only[path] {
			continue
		}
		name := filepath.Base(path)
		switch {
		case newMode == "120000":
			notes = append(notes, path+" is a symlink")
		case newMode == "100755" && fields[0] != ":100755":
			notes = append(notes, path+" is executable")
		}
		for _, s := range sensitive {
			if name == s {
				notes = append(notes, path+" changed")
			}
		}
		for _, d := range sensitiveDirs {
			if strings.HasPrefix(path, d) || strings.Contains(path, "/"+d) {
				notes = append(notes, path+" changed")
				break
			}
		}
	}
	return notes, nil
}
