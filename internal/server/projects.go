package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerProjectWork serves the owner's direct controls over a project: its
// brief, its team, and how its work is run and landed.
func registerProjectWork(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("PUT /api/projects/{id}/brief", func(w http.ResponseWriter, r *http.Request) {
		var in core.BriefInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.UpdateBrief(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/title", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title string `json:"title"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectTitle(r.Context(), r.PathValue("id"), in.Title)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/prefix", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prefix string `json:"prefix"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectPrefix(r.Context(), r.PathValue("id"), in.Prefix)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team", func(w http.ResponseWriter, r *http.Request) {
		var in work.TeamChoice
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetTeam(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team/{kind}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Member string `json:"member"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetSeat(r.Context(), r.PathValue("id"), r.PathValue("kind"), in.Member)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team/qa/browser", func(w http.ResponseWriter, r *http.Request) {
		var in core.Browser
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetSeatBrowser(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Run *core.RunRecipe `json:"run"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetRunRecipe(r.Context(), r.PathValue("id"), in.Run)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/team/seats", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Seat string `json:"seat"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.AddSeat(r.Context(), r.PathValue("id"), in.Seat)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/team/seats/{seat}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.RemoveSeat(r.Context(), r.PathValue("id"), r.PathValue("seat"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/parallel", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MaxActive int `json:"max_active"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetParallel(r.Context(), r.PathValue("id"), in.MaxActive)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/stage-limits", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			StageLimits map[string]int `json:"stage_limits"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetStageLimits(r.Context(), r.PathValue("id"), in.StageLimits)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/workspace", func(w http.ResponseWriter, r *http.Request) {
		var in work.Workspace
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetWorkspace(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/landing", func(w http.ResponseWriter, r *http.Request) {
		var in core.LandPolicy
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetLanding(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
}
