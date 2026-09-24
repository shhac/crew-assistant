package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
)

// registerMembers serves the owner's team: the members kept across projects
// and what each has learned.
func registerMembers(mux *http.ServeMux, a *app.App) {
	save := func(w http.ResponseWriter, r *http.Request, id string) {
		var in core.MemberInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SaveMember(r.Context(), id, in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	}
	mux.HandleFunc("POST /api/members", func(w http.ResponseWriter, r *http.Request) { save(w, r, "") })
	mux.HandleFunc("PUT /api/members/{id}", func(w http.ResponseWriter, r *http.Request) { save(w, r, r.PathValue("id")) })
	mux.HandleFunc("DELETE /api/members/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Core.DeleteMember(r.Context(), r.PathValue("id")); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("POST /api/members/{id}/learnings", func(w http.ResponseWriter, r *http.Request) {
		var in core.LearningInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.AddLearning(r.Context(), r.PathValue("id"), core.LearnedByOwner, in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("DELETE /api/members/{id}/learnings/{learning}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.ForgetLearning(r.Context(), r.PathValue("id"), r.PathValue("learning"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
}
