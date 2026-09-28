package server

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

func isMultipart(r *http.Request) bool {
	kind, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return kind == "multipart/form-data"
}

// maxNoteForm bounds a note with its files: the most a task can keep, and
// room for the note's words and the form around them.
const maxNoteForm = core.MaxTaskAttachmentBytes + 1<<20

// noteForm reads a note's words and files from a multipart form: a "text"
// field and any number of "files". Files are read up to the limit and
// judged by core; one over the limit is refused here, with its exact limit,
// without reading the rest of it. On failure it has already answered.
func noteForm(w http.ResponseWriter, r *http.Request) (string, []core.NewFile, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxNoteForm)
	parts, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "invalid request body")
		return "", nil, err
	}
	var text string
	var files []core.NewFile
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			return text, files, nil
		}
		if err != nil {
			return "", nil, formFailed(w, err)
		}
		switch part.FormName() {
		case "text":
			data, err := io.ReadAll(io.LimitReader(part, 64<<10))
			if err != nil {
				return "", nil, formFailed(w, err)
			}
			text = string(data)
		case "files":
			if len(files) == core.MaxAttachmentsPerSet {
				err := fmt.Errorf("A note can have at most %d attachments", core.MaxAttachmentsPerSet)
				fail(w, 400, err.Error())
				return "", nil, err
			}
			data, err := io.ReadAll(io.LimitReader(part, core.MaxAttachmentBytes+1))
			if err != nil {
				return "", nil, formFailed(w, err)
			}
			if len(data) > core.MaxAttachmentBytes {
				err := fmt.Errorf("%s can't be attached: it is larger than %s, the most a file can be", part.FileName(), core.ExactBytes(core.MaxAttachmentBytes))
				fail(w, http.StatusRequestEntityTooLarge, err.Error())
				return "", nil, err
			}
			files = append(files, core.NewFile{Name: part.FileName(), Data: data})
		default:
			err := fmt.Errorf("unknown field %q", part.FormName())
			fail(w, 400, "invalid request body")
			return "", nil, err
		}
	}
}

func formFailed(w http.ResponseWriter, err error) error {
	if tooLarge := new(http.MaxBytesError); errors.As(err, &tooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("A note and its files can come to at most %s", core.ExactBytes(maxNoteForm)))
		return err
	}
	fail(w, 400, "invalid request body")
	return err
}

// serveAttachment sends an attachment as it was kept, under its judged
// type, never sniffed, and sandboxed so nothing in it can run as the
// dashboard. Images and plain text show in the browser; HTML and PDF are
// downloaded.
func serveAttachment(w http.ResponseWriter, r *http.Request, a core.Attachment, path string) {
	f, err := os.Open(path)
	if err != nil {
		fail(w, 404, "The file is missing")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		fail(w, 404, "The file is missing")
		return
	}
	kind, inline := servedAs(a)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	if named := mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}); named != "" {
		disposition = named
	}
	h := w.Header()
	h.Set("Content-Type", kind)
	h.Set("Content-Disposition", disposition)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// An attachment is never changed, so its id always names the same file.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// servedAs is the type an attachment is sent as, and whether it is shown
// rather than downloaded.
func servedAs(a core.Attachment) (string, bool) {
	switch {
	case a.Type == "text/html":
		return "text/html; charset=utf-8", false
	case a.Type == "application/pdf":
		return a.Type, false
	case strings.HasPrefix(a.Type, "image/"):
		return a.Type, true
	case a.IsText():
		return "text/plain; charset=utf-8", true
	}
	return "application/octet-stream", false
}
