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
	mux.HandleFunc("PUT /api/projects/{id}/linear", func(w http.ResponseWriter, r *http.Request) {
		var in core.LinearLink
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectLinear(r.Context(), r.PathValue("id"), &in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/linear", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.ClearProjectLinear(r.Context(), r.PathValue("id"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("GET /api/connections/{id}/linear/{kind}", func(w http.ResponseWriter, r *http.Request) {
		resource := r.URL.Query().Get("team")
		if r.PathValue("kind") == "project-teams" {
			resource = r.URL.Query().Get("project")
		}
		v, err := a.LinearOptions(r.Context(), r.PathValue("id"), r.URL.Query().Get("profile"), r.PathValue("kind"), resource)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/paused", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool `json:"paused"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectPaused(r.Context(), r.PathValue("id"), in.Paused)
		if err == nil && !in.Paused {
			a.Work.Nudge()
		}
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/landing-paused", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetLandingPaused(r.Context(), r.PathValue("id"), in.Paused, in.Reason)
		if err == nil && !in.Paused {
			a.Work.Nudge()
		}
		reply(w, 200, v, err)
	})
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
	mux.HandleFunc("PUT /api/projects/{id}/team/check/loopback", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetCheckLoopback(r.Context(), r.PathValue("id"), in.On)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		var in *core.ReleasePolicy
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetRelease(r.Context(), r.PathValue("id"), in)
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
			v, err := a.Work.AddToRole(r.Context(), r.PathValue("id"), in.Kind, in.Member)
			reply(w, 200, v, err)
			return
		}
		v, err := a.Work.AddSeat(r.Context(), r.PathValue("id"), in.Seat)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/team/seats/{seat}", func(w http.ResponseWriter, r *http.Request) {
		// With a kind, the seat leaves only that role and keeps its others.
		if kind := r.URL.Query().Get("kind"); kind != "" {
			v, err := a.Work.RemoveFromRole(r.Context(), r.PathValue("id"), r.PathValue("seat"), kind)
			reply(w, 200, v, err)
			return
		}
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
