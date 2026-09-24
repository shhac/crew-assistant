package server

import (
	"errors"
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/filesystem"
	"net/http"
	"os"
)

func registerFilesystem(mux *http.ServeMux, a *app.App) {
	home, _ := os.UserHomeDir()
	browser := filesystem.New(home)
	mux.HandleFunc("GET /api/filesystem", func(w http.ResponseWriter, r *http.Request) {
		if a.Demo {
			fail(w, http.StatusForbidden, "Filesystem browsing is unavailable in demo mode")
			return
		}
		q := r.URL.Query()
		hidden := q.Get("hidden")
		if hidden != "" && hidden != "true" && hidden != "false" {
			fail(w, 400, "hidden must be true or false")
			return
		}
		listing, err := browser.List(r.Context(), filesystem.Query{Path: q.Get("path"), Kind: q.Get("kind"), Cursor: q.Get("cursor"), Hidden: hidden == "true"})
		if err != nil {
			switch {
			case errors.Is(err, filesystem.ErrCursor):
				fail(w, 409, err.Error())
			case errors.Is(err, os.ErrPermission):
				fail(w, 403, "crew-assistant can't read this folder")
			case errors.Is(err, os.ErrNotExist):
				fail(w, 404, "There's no such folder")
			case errors.Is(err, filesystem.ErrTooLarge), errors.Is(err, filesystem.ErrKind), errors.Is(err, filesystem.ErrPath):
				fail(w, 400, err.Error())
			default:
				fail(w, 400, "Can't open this folder")
			}
			return
		}
		respond(w, 200, listing)
	})
	mux.HandleFunc("PUT /api/projects/{id}/directories", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Directories []string `json:"directories"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.Core.SetProjectDirectories(r.Context(), r.PathValue("id"), in.Directories)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, result)
	})
}
