package localdocs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shhac/crew-assistant/internal/media"
)

// File is one file of a revision as shown to the owner.
type File = media.File

// Preview returns revision n's files, each cut at limit bytes.
func (d Docs) Preview(taskID string, n int, limit int) ([]File, error) {
	root := d.revision(taskID, n)
	out := []File{}
	err := walkFiles(root, func(rel string, info fs.FileInfo) error {
		file := File{Path: rel, Size: info.Size()}
		raw, err := readLimited(filepath.Join(root, rel), limit)
		if err != nil {
			return err
		}
		file.Truncated = info.Size() > int64(len(raw))
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			file.Binary = true
		} else {
			file.Content = string(raw)
		}
		out = append(out, file)
		return nil
	})
	return out, err
}

// Deliver copies revision n into its own folder under dest and returns it.
// Nothing already in dest is overwritten. A folder left by an earlier attempt
// that matches the revision exactly counts as delivered, so a retried
// delivery settles rather than repeats.
func (d Docs) Deliver(taskID string, n int, dest, label string) (string, error) {
	if !filepath.IsAbs(dest) {
		return "", errors.New("delivery folder must be absolute")
	}
	info, err := os.Stat(dest)
	if err != nil || !info.IsDir() {
		return "", errors.New("delivery folder does not exist")
	}
	source := d.revision(taskID, n)
	want, err := digest(source)
	if err != nil {
		return "", err
	}
	base := slug(label) + "-r" + strconv.Itoa(n)
	for attempt := 0; attempt < 100; attempt++ {
		name := base
		if attempt > 0 {
			name = base + "-" + strconv.Itoa(attempt+1)
		}
		target := filepath.Join(dest, name)
		if _, statErr := os.Lstat(target); statErr == nil {
			if got, digestErr := digest(target); digestErr == nil && got == want {
				return target, nil
			}
			continue
		}
		if _, err = copyTree(source, target); err != nil {
			return "", err
		}
		return target, nil
	}
	return "", errors.New("no free delivery folder name")
}

func slug(label string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "draft"
	}
	return out
}
