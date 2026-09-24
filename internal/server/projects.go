package server

import (
	"net/http"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerProjectWork serves the owner's direct controls over a project: its
// brief, its team, the outcomes asked of it, and what those produced.
func registerProjectWork(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("PUT /api/projects/{id}/brief", func(w http.ResponseWriter, r *http.Request) {
		var in core.BriefInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.UpdateBrief(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		a.Work.Nudge()
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team", func(w http.ResponseWriter, r *http.Request) {
		var in work.TeamChoice
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetTeam(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		a.Work.Nudge()
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/landing", func(w http.ResponseWriter, r *http.Request) {
		var in core.LandPolicy
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetLanding(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/land", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.LandTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		var in core.TaskInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.QueueTask(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		a.Work.Nudge()
		respond(w, 201, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/stop", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.StopTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/revisions/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 {
			fail(w, 400, "invalid revision")
			return
		}
		files, err := a.Work.RevisionPreview(r.Context(), r.PathValue("id"), r.PathValue("task"), n)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]any{"files": files})
	})
}
