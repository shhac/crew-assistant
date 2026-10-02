package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/catalog"
)

// registerMembers serves the owner's team: the members kept across projects
// and what each has learned.
func registerMembers(mux *http.ServeMux, a *app.App, lookups ...*modelLookup) {
	lookup := newModelLookup(catalog.Discover)
	if len(lookups) > 0 {
		lookup = lookups[0]
	}
	mux.HandleFunc("POST /api/members", func(w http.ResponseWriter, r *http.Request) {
		var in core.MemberInput
		if decode(w, r, &in) != nil {
			return
		}
		if err := checkMemberModel(r, a, lookup, in); err != nil {
			problem(w, err)
			return
		}
		v, err := a.CreateMember(r.Context(), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/members/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in core.MemberInput
		if decode(w, r, &in) != nil {
			return
		}
		if err := checkMemberModel(r, a, lookup, in); err != nil {
			problem(w, err)
			return
		}
		v, err := a.Core.SaveMember(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/members/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"deleted": true}, a.Core.DeleteMember(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/members/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Look string `json:"look"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		reply(w, 202, map[string]bool{"drawing": true}, a.DrawMember(r.Context(), r.PathValue("id"), in.Look))
	})
	registerAssistants(mux, a)
	// Avatars are named by a hash of the picture, so a name never shows a
	// different picture and the browser may keep it.
	mux.HandleFunc("GET /api/avatars/{id}/{size}", func(w http.ResponseWriter, r *http.Request) {
		path, ok := a.Avatars().Path(r.PathValue("id"), r.PathValue("size"))
		if !ok {
			fail(w, 404, "No such picture")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeFile(w, r, path)
	})
	mux.HandleFunc("POST /api/members/{id}/learnings", func(w http.ResponseWriter, r *http.Request) {
		var in core.LearningInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.AddLearning(r.Context(), r.PathValue("id"), core.LearnedByOwner, in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/members/{id}/learnings/{learning}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.ForgetLearning(r.Context(), r.PathValue("id"), r.PathValue("learning"))
		reply(w, 200, v, err)
	})
}

// checkMemberModel refuses only a catalog's definite tool-calling denial.
// Discovery failure or missing metadata is not evidence against a model.
func checkMemberModel(r *http.Request, a *app.App, lookup *modelLookup, in core.MemberInput) error {
	if err := config.CheckRoleModel(in.Engine, in.Model); err != nil {
		return err
	}
	if in.Engine != "openai-compatible" || a.Demo {
		return nil
	}
	cfg := a.Config()
	if err := cfg.Engines.CheckProvider(in.Engine, in.Provider); err != nil {
		return err
	}
	entry := lookup.get(r.Context(), cfg.HarnessOn(in.Engine, in.Provider, in.Model, in.Effort))
	if entry.detail != "" {
		return nil
	}
	for _, m := range entry.value {
		if m.ID == strings.TrimSpace(in.Model) {
			if tools, known := catalog.SupportsTools(m); known && !tools {
				return errors.New("This model can't call tools, so it can't work on a team. Choose a model that lists tool calling.")
			}
		}
	}
	return nil
}
