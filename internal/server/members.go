package server

import (
	"context"
	"net/http"
	"os"

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
		save := a.Core.SaveMember
		if id == "" {
			save = func(ctx context.Context, _ string, in core.MemberInput) (core.Member, error) {
				return a.CreateMember(ctx, in)
			}
		}
		v, err := save(r.Context(), id, in)
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
	mux.HandleFunc("POST /api/members/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Look string `json:"look"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if err := a.DrawMember(r.Context(), r.PathValue("id"), in.Look); err != nil {
			problem(w, err)
			return
		}
		respond(w, 202, map[string]bool{"drawing": true})
	})
	mux.HandleFunc("POST /api/assistant/avatar", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Look string `json:"look"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if err := a.DrawAssistant(r.Context(), in.Look); err != nil {
			problem(w, err)
			return
		}
		respond(w, 202, map[string]bool{"drawing": true})
	})
	// Avatars are named by a hash of the picture, so a name never shows a
	// different picture and the browser may keep it.
	mux.HandleFunc("GET /api/avatars/{id}/{size}", func(w http.ResponseWriter, r *http.Request) {
		path, ok := a.Avatars().Path(r.PathValue("id"), r.PathValue("size"))
		if !ok {
			fail(w, 404, "No such picture")
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fail(w, 404, "No such picture")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Write(data)
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
