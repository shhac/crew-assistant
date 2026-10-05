package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/toolkit"
)

// These routes run behind New's owner authentication and same-origin check.
// A request names a tool and an action from the catalog; it never supplies
// any part of the command that runs.
func registerToolkit(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/toolkit", func(w http.ResponseWriter, r *http.Request) {
		refresh := r.URL.Query().Get("refresh")
		if refresh != "" && refresh != "true" {
			fail(w, 400, "refresh must be true")
			return
		}
		overview, err := a.ToolkitOverview(r.Context(), refresh == "true")
		toolkitReply(w, 200, overview, err)
	})
	mux.HandleFunc("POST /api/toolkit/update-all", func(w http.ResponseWriter, r *http.Request) {
		job, err := a.UpdateAllTools(r.Context())
		toolkitReply(w, 202, job, err)
	})
	mux.HandleFunc("POST /api/toolkit/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		job, err := a.StartToolkitJob(r.PathValue("id"), r.PathValue("action"))
		toolkitReply(w, 202, job, err)
	})
	mux.HandleFunc("GET /api/toolkit/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		after := 0
		if raw := r.URL.Query().Get("after"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 {
				fail(w, 400, "after must be a line number")
				return
			}
			after = n
		}
		job, err := a.ToolkitJob(r.PathValue("id"), after)
		toolkitReply(w, 200, job, err)
	})
}

func toolkitReply(w http.ResponseWriter, status int, v any, err error) {
	switch {
	case err == nil:
		respond(w, status, v)
	case errors.Is(err, app.ErrToolkitDemo):
		fail(w, http.StatusForbidden, ownerText(err))
	case errors.Is(err, toolkit.ErrUnknownTool), errors.Is(err, toolkit.ErrUnknownAction), errors.Is(err, toolkit.ErrUnknownJob):
		fail(w, http.StatusNotFound, ownerText(err))
	case errors.Is(err, toolkit.ErrBusy), errors.Is(err, toolkit.ErrNoHomebrew), errors.Is(err, toolkit.ErrNoNPX),
		errors.Is(err, toolkit.ErrUnavailable), errors.Is(err, toolkit.ErrNotInstalled), errors.Is(err, toolkit.ErrUpToDate):
		fail(w, http.StatusConflict, ownerText(err))
	default:
		problem(w, err)
	}
}
