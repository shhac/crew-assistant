package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerStaffing serves the owner's repositories and teams, and which of
// them each project works in and is staffed by. Both are listed in the
// state; several projects may share one.
func registerStaffing(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("POST /api/repositories", func(w http.ResponseWriter, r *http.Request) {
		var in core.RepositoryInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.CreateRepository(r.Context(), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/repositories/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in core.RepositoryInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.UpdateRepository(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/repositories/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"deleted": true}, a.Core.DeleteRepository(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/teams", func(w http.ResponseWriter, r *http.Request) {
		var in core.TeamInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.CreateTeam(r.Context(), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in core.TeamUpdate
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.UpdateTeam(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"deleted": true}, a.Core.DeleteTeam(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("PUT /api/teams/{id}/seats/{kind}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Member string `json:"member"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetTeamSeat(r.Context(), r.PathValue("id"), r.PathValue("kind"), in.Member)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/teams/{id}/seats", func(w http.ResponseWriter, r *http.Request) {
		// A seat to copy, or someone to add to a role: a member, or the
		// template's seat when member is empty.
		var in struct {
			Seat   string `json:"seat"`
			Kind   string `json:"kind"`
			Member string `json:"member"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if in.Kind != "" {
			v, err := a.Work.AddToTeamRole(r.Context(), r.PathValue("id"), in.Kind, in.Member)
			reply(w, 200, v, err)
			return
		}
		v, err := a.Work.AddTeamSeat(r.Context(), r.PathValue("id"), in.Seat)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/teams/{id}/seats/{seat}", func(w http.ResponseWriter, r *http.Request) {
		// With a kind, the seat leaves only that role and keeps its others.
		v, err := a.Work.RemoveTeamSeat(r.Context(), r.PathValue("id"), r.PathValue("seat"), r.URL.Query().Get("kind"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
		var in core.ProjectSetup
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectSetup(r.Context(), r.PathValue("id"), in)
		if err == nil {
			a.Work.Nudge()
		}
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/seat-overrides", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Overrides []work.SeatOverrideChoice `json:"overrides"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetSeatOverrides(r.Context(), r.PathValue("id"), in.Overrides)
		reply(w, 200, v, err)
	})
}
