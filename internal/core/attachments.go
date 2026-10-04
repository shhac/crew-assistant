package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shhac/crew-assistant/internal/statepath"
	"github.com/shhac/crew-assistant/internal/text"
	_ "golang.org/x/image/webp"
)

// Attachment is a file kept with a task: one the owner added with a note,
// or the designer with a design. Every task keeps one list of them, so
// limits, serving and what roles may read share one path, and each record
// says where it came from: who added it, when, and with which note or
// design. The file itself is kept under the state directory, never in a
// workspace.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Made string `json:"made,omitempty"`
	// Type is the media type judged from the file's bytes and its name,
	// never from what the sender claimed.
	Type string `json:"type"`
	Size int64  `json:"size"`
	// By and Kind are who added it and as what, as for a note.
	By   string    `json:"by"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Exactly one of Note, Design and Verdict is set: the note it came with,
	// the design request whose input it belongs to, or the check whose
	// evidence it is.
	Note    string `json:"note,omitempty"`
	Design  string `json:"design,omitempty"`
	Verdict string `json:"verdict,omitempty"`
}

// NewFile is a file to attach, as it arrived.
type NewFile struct {
	Name string
	Made string
	Data []byte
}

// Limits on attachments. The dashboard mirrors them, so it can refuse a
// file with the same reason before sending it.
const (
	MaxAttachmentBytes   = 5 << 20
	MaxAttachmentsPerSet = 10
	// A task producing assets keeps every frame, attempt and provenance
	// record, so its room is generous; one note is still bounded above.
	MaxTaskAttachments     = 120
	MaxTaskAttachmentBytes = 250 << 20
	maxAttachmentName      = 120
	// maxImageSide keeps a hostile image from costing much to show.
	maxImageSide = 10000
)

// attachmentTypes is what can be attached, by the name's extension. Raster
// images and PDF must also be that by their bytes; the rest must be UTF-8
// text with no NUL bytes.
var attachmentTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".pdf": "application/pdf",
	".md":  "text/markdown", ".markdown": "text/markdown", ".txt": "text/plain", ".json": "application/json",
	".csv": "text/csv", ".svg": "image/svg+xml", ".html": "text/html", ".htm": "text/html",
}

// AttachmentKinds says what can be attached, as a refusal explains it.
const AttachmentKinds = "images (PNG, JPEG, GIF, WebP), PDF, and text files (Markdown, plain text, JSON, CSV, SVG, HTML)"

// IsText reports a type kept as UTF-8 text.
func (a Attachment) IsText() bool {
	return strings.HasPrefix(a.Type, "text/") || a.Type == "application/json" || a.Type == "image/svg+xml"
}

// checkFile judges a file on its own: a safe name, a size within the limit
// and a type its bytes bear out. It returns the file's record, without an
// id or its origin.
func checkFile(f NewFile) (Attachment, error) {
	name, err := attachmentName(f.Name)
	if err != nil {
		return Attachment{}, err
	}
	refuse := func(why string, args ...any) error {
		return fmt.Errorf("%s can't be attached: %s", name, fmt.Sprintf(why, args...))
	}
	switch size := len(f.Data); {
	case size == 0:
		return Attachment{}, refuse("it is empty")
	case size > MaxAttachmentBytes:
		return Attachment{}, refuse("it is %s, and a file can be at most %s", ExactBytes(int64(size)), ExactBytes(MaxAttachmentBytes))
	}
	kind, ok := attachmentTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return Attachment{}, refuse("only %s can be attached", AttachmentKinds)
	}
	switch {
	case kind == "application/pdf":
		if !bytes.HasPrefix(f.Data, []byte("%PDF-")) {
			return Attachment{}, refuse("it is not a PDF")
		}
	case strings.HasPrefix(kind, "image/") && kind != "image/svg+xml":
		cfg, format, err := image.DecodeConfig(bytes.NewReader(f.Data))
		if err != nil || "image/"+format != kind {
			return Attachment{}, refuse("it is not a %s image", strings.ToUpper(strings.TrimPrefix(kind, "image/")))
		}
		if cfg.Width > maxImageSide || cfg.Height > maxImageSide {
			return Attachment{}, refuse("it is %d×%d, and an image can be at most %d pixels a side", cfg.Width, cfg.Height, maxImageSide)
		}
	default:
		if !utf8.Valid(f.Data) {
			return Attachment{}, refuse("it is not UTF-8 text")
		}
		if bytes.IndexByte(f.Data, 0) >= 0 {
			return Attachment{}, refuse("it contains binary data")
		}
	}
	return Attachment{Name: name, Made: f.Made, Type: kind, Size: int64(len(f.Data))}, nil
}

// attachmentName is a file's name as kept, which is only ever shown: it
// never names a path.
func attachmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("an attachment needs a name")
	case !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl):
		return "", fmt.Errorf("%q can't be attached: its name has characters a name can't hold", name)
	case strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%s can't be attached: a name can't be a path or start with a dot", name)
	case len(name) > maxAttachmentName:
		return "", fmt.Errorf("%s… can't be attached: a name can be at most %d bytes", name[:40], maxAttachmentName)
	}
	return name, nil
}

// ExactBytes is a size as a limit reports it: exactly, since a rounded size
// can read as within the limit.
func ExactBytes(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s + " bytes"
}

var attachmentID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// AttachmentsDirectory is where a task's attachments are kept, which roles
// working on it may read.
func (s *Service) AttachmentsDirectory(taskID string) string {
	return filepath.Join(s.StateDirectory(), "attachments", taskID)
}

// attachmentPath is where one attachment is kept, if the ids are ones the
// store could have made.
func (s *Service) attachmentPath(taskID, id string) (string, bool) {
	if !attachmentID.MatchString(taskID) || !attachmentID.MatchString(id) {
		return "", false
	}
	return filepath.Join(s.AttachmentsDirectory(taskID), id), true
}

// attach keeps files for a task, then records them with change in one store
// update. Every file is checked before any is written, so a set is never
// partly accepted, and if the update fails the files written are removed.
// change gets the records, with their ids, to complete and keep.
func (s *Service) attach(ctx context.Context, taskRef string, files []NewFile, change func(v *Snapshot, t *Task, kept []Attachment, now time.Time) error) error {
	kept, written, err := s.writeFiles(ctx, taskRef, files)
	if err != nil {
		return err
	}
	err = s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, strings.TrimSpace(taskRef))
		if t == nil {
			return ErrNotFound
		}
		if err := withinLimits(t, kept); err != nil {
			return err
		}
		now := s.now().UTC()
		for i := range kept {
			kept[i].At = now
		}
		return change(v, t, kept, now)
	})
	if err != nil {
		removeAll(written)
	}
	return err
}

// writeFiles checks files and writes them where the task's attachments are
// kept, before anything records them. Every file is checked before any is
// written, and if one can't be written those already written are removed.
// It gives the files' records, with their ids, and where they were written.
func (s *Service) writeFiles(ctx context.Context, taskRef string, files []NewFile) ([]Attachment, []string, error) {
	if len(files) > MaxAttachmentsPerSet {
		return nil, nil, fmt.Errorf("%d files can't be attached at once; the most is %d", len(files), MaxAttachmentsPerSet)
	}
	kept := make([]Attachment, len(files))
	for i, f := range files {
		a, err := checkFile(f)
		if err != nil {
			return nil, nil, err
		}
		a.ID = uid()
		kept[i] = a
	}
	var written []string
	if len(files) > 0 {
		snap, err := s.Snapshot(ctx)
		if err != nil {
			return nil, nil, err
		}
		t, ok := snap.FindTask(strings.TrimSpace(taskRef))
		if !ok {
			return nil, nil, ErrNotFound
		}
		for i, f := range files {
			path, err := s.keepFile(t.ID, kept[i].ID, f.Data)
			if err != nil {
				removeAll(written)
				return nil, nil, err
			}
			written = append(written, path)
		}
	}
	return kept, written, nil
}

// withinLimits refuses records that would take a task past what it can
// keep in all.
func withinLimits(t *Task, kept []Attachment) error {
	count, total := len(t.Attachments), int64(0)
	for _, a := range t.Attachments {
		total += a.Size
	}
	for _, a := range kept {
		count, total = count+1, total+a.Size
	}
	switch {
	case count > MaxTaskAttachments:
		return fmt.Errorf("a task can keep at most %d attachments, and this one has %d", MaxTaskAttachments, len(t.Attachments))
	case total > MaxTaskAttachmentBytes:
		return fmt.Errorf("a task can keep at most %s of attachments, and these would bring it to %s", ExactBytes(MaxTaskAttachmentBytes), ExactBytes(total))
	}
	return nil
}

// keepFile writes one attachment's bytes where it is kept: to a temporary
// file first, renamed into place, so a file is never seen half written.
func (s *Service) keepFile(taskID, id string, data []byte) (string, error) {
	path, ok := s.attachmentPath(taskID, id)
	if !ok {
		return "", errors.New("the attachment could not be named")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := statepath.WriteFileAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func removeAll(paths []string) {
	for _, p := range paths {
		os.Remove(p)
	}
}

// DesignFiles are files the designer attaches to the input it is giving.
type DesignFiles struct {
	Project, Task string
	By, Kind      string
	// While is the status the task must still be in; Design is the open
	// request the files belong to.
	While, Design string
	Files         []NewFile
}

// AttachToDesign keeps files with the design input the designer is giving.
// Once the request is answered, or the task has moved on, nothing more can
// be attached to it.
func (s *Service) AttachToDesign(ctx context.Context, in DesignFiles) ([]Attachment, error) {
	if len(in.Files) == 0 {
		return nil, errors.New("there is no file to attach")
	}
	var out []Attachment
	err := s.attach(ctx, in.Task, in.Files, func(v *Snapshot, t *Task, kept []Attachment, now time.Time) error {
		r := t.OpenDesign()
		switch {
		case t.ProjectID != in.Project:
			return ErrNotFound
		case in.While != "" && t.Status != in.While:
			return fmt.Errorf("the task has moved on, so this changes nothing more: %w", ErrConflict)
		case t.Status != TaskDesigning || r == nil || r.ID != in.Design:
			return fmt.Errorf("that design input has been given, so nothing more can be attached to it: %w", ErrConflict)
		}
		already := 0
		for _, a := range t.Attachments {
			if a.Design == r.ID {
				already++
			}
		}
		if already+len(kept) > MaxAttachmentsPerSet {
			return fmt.Errorf("a design can have at most %d attachments, and this one has %d", MaxAttachmentsPerSet, already)
		}
		names := make([]string, len(kept))
		for i := range kept {
			kept[i].By, kept[i].Kind, kept[i].Design = in.By, in.Kind, r.ID
			names[i] = kept[i].Name
		}
		t.Attachments = append(t.Attachments, kept...)
		t.UpdatedAt = now
		recordTask(v, now, t, "task.attached", fmt.Sprintf("%s attached %s to design input on %s", in.By, strings.Join(names, ", "), t.Objective))
		derive(v, t)
		out = kept
		return nil
	})
	return out, err
}

// Screenshots are what QA's turn took while it used the app, to keep as the
// evidence of its verdict: the images as files, who took them, and how many
// more were taken and not kept. They are recorded in the same update as the
// verdict, and only if the verdict is, so none ever names a verdict that
// does not exist.
type Screenshots struct {
	By      string
	Files   []NewFile
	Omitted int
}

// pendingShots are screenshots written where they are kept, for the update
// that records their verdict to keep or let go.
type pendingShots struct {
	shots   Screenshots
	kept    []Attachment
	written []string
	// failed is why the files can't be kept, which the verdict says instead.
	failed   error
	recorded bool
}

// writeScreenshots checks and writes a check's screenshots before the update
// that records its verdict. Files that can't be kept are not refused: the
// verdict says why instead.
func (s *Service) writeScreenshots(ctx context.Context, taskRef string, shots Screenshots) *pendingShots {
	p := &pendingShots{shots: shots}
	switch {
	case len(shots.Files) == 0:
	case len(shots.Files) > MaxScreenshots:
		p.failed = fmt.Errorf("a check can keep at most %d screenshots", MaxScreenshots)
	default:
		p.kept, p.written, p.failed = s.writeFiles(ctx, taskRef, shots.Files)
	}
	return p
}

// evidence gives verdict its screenshots, inside the update that records
// it: an id, and an item for each screenshot, or why they couldn't be kept,
// then how many more were taken.
func (p *pendingShots) evidence(t *Task, verdict *Verdict, now time.Time) {
	verdict.ID = ""
	if n := len(p.shots.Files); n > 0 {
		failed := p.failed
		if failed == nil {
			failed = withinLimits(t, p.kept)
		}
		if failed != nil {
			verdict.Evidence = append(verdict.Evidence, Evidence{Kind: EvidenceScreenshot, Text: text.Clip(fmt.Sprintf("%d screenshots could not be kept: %v", n, failed), MaxEvidenceText)})
		} else {
			verdict.ID = uid()
			for i := range p.kept {
				p.kept[i].By, p.kept[i].Kind, p.kept[i].Verdict, p.kept[i].At = p.shots.By, RoleQA, verdict.ID, now
				verdict.Evidence = append(verdict.Evidence, Evidence{Kind: EvidenceScreenshot, Attachment: p.kept[i].ID})
			}
		}
	}
	if p.shots.Omitted > 0 {
		verdict.Evidence = append(verdict.Evidence, Evidence{Kind: EvidenceScreenshot, Text: fmt.Sprintf("%d more screenshots were taken and not kept: a check keeps its last %d, each an image within the attachment limits.", p.shots.Omitted, MaxScreenshots)})
	}
}

// keep records the screenshots with the task, in the same update, if the
// verdict they were given is among its verdicts now.
func (p *pendingShots) keep(v *Snapshot, t *Task, verdictID string, now time.Time) {
	p.recorded = false
	if verdictID == "" || !slices.ContainsFunc(t.Verdicts, func(v Verdict) bool { return v.ID == verdictID }) {
		return
	}
	names := make([]string, len(p.kept))
	for i, a := range p.kept {
		names[i] = a.Name
	}
	t.Attachments = append(t.Attachments, p.kept...)
	recordTask(v, now, t, "task.attached", fmt.Sprintf("%s kept %s from checking %s", p.shots.By, strings.Join(names, ", "), t.Objective))
	p.recorded = true
}

// done removes the files written, unless the update that ended with err
// recorded them.
func (p *pendingShots) done(err error) {
	if err != nil || !p.recorded {
		removeAll(p.written)
	}
}

// OpenAttachment finds one of a task's attachments, and where its file is
// kept. An id from another task, or another project, is not found.
func (s *Service) OpenAttachment(ctx context.Context, projectID, taskRef, id string) (Attachment, string, error) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return Attachment{}, "", err
	}
	t, ok := snap.FindTask(strings.TrimSpace(taskRef))
	if !ok || t.ProjectID != projectID {
		return Attachment{}, "", ErrNotFound
	}
	for _, a := range t.Attachments {
		if a.ID == id {
			path, ok := s.attachmentPath(t.ID, a.ID)
			if !ok {
				return Attachment{}, "", ErrNotFound
			}
			return a, path, nil
		}
	}
	return Attachment{}, "", ErrNotFound
}
