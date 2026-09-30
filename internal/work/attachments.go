package work

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

// attachmentsIndex tells a role where each of its task's files is kept, so
// it can open them, images included, and what each came with: which design,
// and whether that is the current one, or which note.
func attachmentsIndex(t core.Task, dir string) string {
	if len(t.Attachments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Files attached to this task, which you can open and read (images too); open them only to read:\n")
	for _, a := range t.Attachments {
		fmt.Fprintf(&b, "- %s: %s, %s\n", filepath.Join(dir, a.ID), a.Name, attachedWith(t, a))
	}
	return b.String()
}

// attachedWith says what an attachment came with, as a role reads it.
func attachedWith(t core.Task, a core.Attachment) string {
	if a.Note != "" {
		for i, n := range t.Notes {
			if n.ID == a.Note {
				return fmt.Sprintf("with note %d, by %s", i+1, n.By)
			}
		}
		return "with a note"
	}
	if a.Verdict != "" {
		for _, v := range t.Verdicts {
			if v.ID == a.Verdict {
				return fmt.Sprintf("a screenshot %s took checking draft %d", v.Role, v.Revision)
			}
		}
		return "a screenshot QA took checking a draft"
	}
	for _, r := range t.Design {
		if r.ID != a.Design {
			continue
		}
		switch {
		case r.ID == t.CurrentDesign:
			return fmt.Sprintf("with design %d, the current design and the target", r.N)
		case r.Marked:
			return fmt.Sprintf("with design %d, superseded: not current and not the target", r.N)
		case r.N > 0:
			return fmt.Sprintf("with design %d, advice rather than a design to build to", r.N)
		}
		return "with design input still being given"
	}
	return "with design input"
}

// workspaceFile reads a file the designer names in its workspace, for
// attach_file. The path is resolved inside dir only: no symlink or ".."
// leads out of it, and only a regular file of at most the attachment limit
// is read. Nothing is ever run.
func workspaceFile(dir, path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("path must name a file inside the current directory")
		}
		path = rel
	}
	return readInside(dir, path, "the current directory")
}

// readInside reads a regular file of at most the attachment limit at path
// inside dir, which where names for the role; no symlink or ".." leads out.
func readInside(dir, path, where string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s can't be read from %s: it is missing, or leads outside it", path, where)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	return io.ReadAll(io.LimitReader(f, core.MaxAttachmentBytes+1))
}
