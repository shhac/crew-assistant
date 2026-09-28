package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

type formFile struct {
	name string
	data []byte
}

// formServer is ownerApp's server, which also sends a note as a multipart
// form, as the dashboard does with files.
func formServer(t *testing.T) (*app.App, func(method, path, body string) *httptest.ResponseRecorder, func(path, text string, files ...formFile) *httptest.ResponseRecorder) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	store, err := core.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(dir, "config.json"), app.Options{})
	auth, _ := NewAuth(dir, "http://127.0.0.1:8340", "", nil)
	h := New(a, auth)
	send := func(method, path, kind string, body *bytes.Buffer) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8340"+path, body)
		r.RemoteAddr = "127.0.0.1:4321"
		r.Header.Set("Authorization", "Bearer "+auth.admin)
		r.Header.Set("X-Requested-With", "crew-assistant")
		if kind != "" {
			r.Header.Set("Content-Type", kind)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return send(method, path, "", bytes.NewBufferString(body))
	}
	form := func(path, text string, files ...formFile) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("text", text)
		for _, f := range files {
			part, _ := mw.CreateFormFile("files", f.name)
			part.Write(f.data)
		}
		mw.Close()
		return send("POST", path, mw.FormDataContentType(), &body)
	}
	return a, call, form
}

func TestTheOwnerAttachesFilesToANoteAndTheyAreServedSafely(t *testing.T) {
	a, call, form := formServer(t)
	var project core.Project
	w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &project)
	var task, other core.Task
	for _, into := range []*core.Task{&task, &other} {
		w = call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Schema","criteria":[]}`)
		_ = json.Unmarshal(w.Body.Bytes(), into)
	}
	var shot bytes.Buffer
	png.Encode(&shot, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	notes := "/api/projects/" + project.ID + "/tasks/" + task.ID + "/notes"
	var note core.Note
	w = form(notes, "The layout I mean", formFile{"layout.png", shot.Bytes()}, formFile{"copy.md", []byte("# Copy")}, formFile{"mock.html", []byte("<script>alert(1)</script>")})
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &note) != nil || note.Text != "The layout I mean" {
		t.Fatal(w.Code, w.Body.String())
	}
	snap, _ := a.Snapshot(context.Background())
	kept, _ := snap.FindTask(task.ID)
	if len(kept.Attachments) != 3 || kept.Attachments[0].Note != note.ID {
		t.Fatalf("attachments %+v", kept.Attachments)
	}
	path := func(taskID, id string) string {
		return "/api/projects/" + project.ID + "/tasks/" + taskID + "/attachments/" + id
	}
	for i, want := range []struct{ kind, disposition string }{
		{"image/png", `inline; filename=layout.png`},
		{"text/plain; charset=utf-8", `inline; filename=copy.md`},
		{"text/html; charset=utf-8", `attachment; filename=mock.html`},
	} {
		w := call("GET", path(task.Ref, kept.Attachments[i].ID), "")
		h := w.Header()
		if w.Code != 200 || h.Get("Content-Type") != want.kind || h.Get("Content-Disposition") != want.disposition || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
			t.Errorf("%s: %d %v", kept.Attachments[i].Name, w.Code, h)
		}
	}
	if w := call("GET", path(task.ID, kept.Attachments[0].ID), ""); !bytes.Equal(w.Body.Bytes(), shot.Bytes()) {
		t.Fatal("the file served is not the file sent")
	}
	for name, p := range map[string]string{
		"another task's":  path(other.ID, kept.Attachments[0].ID),
		"a path":          path(task.ID, "..%2F..%2Fstate.db"),
		"a made-up id":    path(task.ID, strings.Repeat("0", 24)),
		"another project": "/api/projects/nope/tasks/" + task.ID + "/attachments/" + kept.Attachments[0].ID,
	} {
		if w := call("GET", p, ""); w.Code != 404 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	// The JSON note is as it was.
	if w := call("POST", notes, `{"text":"Plain words"}`); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestANoteWithFilesPastTheLimitsIsRefusedWhole(t *testing.T) {
	a, call, form := formServer(t)
	var project core.Project
	w := call("POST", "/api/projects", `{"title":"Export","brief":{"goal":"CSV","criteria":["Valid CSV"]},"template":"draft"}`)
	_ = json.Unmarshal(w.Body.Bytes(), &project)
	var task core.Task
	w = call("POST", "/api/projects/"+project.ID+"/tasks", `{"objective":"Schema","criteria":[]}`)
	_ = json.Unmarshal(w.Body.Bytes(), &task)
	notes := "/api/projects/" + project.ID + "/tasks/" + task.ID + "/notes"
	small := formFile{"fine.md", []byte("fine")}
	eleven := make([]formFile, core.MaxAttachmentsPerSet+1)
	for i := range eleven {
		eleven[i] = small
	}
	for name, c := range map[string]struct {
		files []formFile
		code  int
		says  string
	}{
		"a file too large": {[]formFile{small, {"big.txt", bytes.Repeat([]byte("a"), core.MaxAttachmentBytes+1)}}, 413, "big.txt can't be attached: it is larger than 5,242,880 bytes"},
		"too many files":   {eleven, 400, "at most 10 attachments"},
		"text as a PNG":    {[]formFile{small, {"mock.png", []byte("words")}}, 400, "Mock.png can't be attached: it is not a PNG image"},
		"a program":        {[]formFile{{"run.sh", []byte("echo")}}, 400, "only images"},
	} {
		w := form(notes, "See these", c.files...)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	snap, _ := a.Snapshot(context.Background())
	kept, _ := snap.FindTask(task.ID)
	if len(kept.Notes) != 0 || len(kept.Attachments) != 0 {
		t.Fatalf("a refused note was kept: %+v %+v", kept.Notes, kept.Attachments)
	}
}
