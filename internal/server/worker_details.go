package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
)

func workerDetailRoutes(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/projects/{id}/workers", func(w http.ResponseWriter, r *http.Request) {
		workers, err := a.WorkerDetails(r.Context(), r.PathValue("id"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusOK, struct {
			Workers []app.WorkerDetail `json:"workers"`
		}{Workers: workers})
	})
	mux.HandleFunc("PUT /api/projects/{id}/workers/{profile}", func(w http.ResponseWriter, r *http.Request) {
		var in app.WorkerUpdate
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.UpdateWorker(r.Context(), r.PathValue("id"), r.PathValue("profile"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusOK, result)
	})
}
