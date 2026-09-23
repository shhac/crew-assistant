package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/dashboard"
)

func New(a *app.App, auth *Auth) http.Handler {
	mux := http.NewServeMux()
	registerFilesystem(mux, a)
	registerModels(mux, a)
	registerChatQueue(mux, a)
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Snapshot(r.Context())
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, struct {
			core.Snapshot
			Demo bool `json:"demo"`
		}{s, a.Demo})
	})
	mux.HandleFunc("POST /api/operations/{id}/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Note string `json:"note"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if err := a.Core.AcknowledgeEvent(r.Context(), r.PathValue("id"), in.Note); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]bool{"acknowledged": true})
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.Config()) })
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c config.Config
		if decode(w, r, &c) != nil {
			return
		}
		if err := a.UpdateConfig(c); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, c)
	})
	mux.HandleFunc("GET /api/connection-profiles", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.DiscoverConnectionProfiles(r.Context(), r.URL.Query().Get("tool"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, result)
	})
	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) {
		state, err := a.IdentitySetup()
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, state)
	})
	mux.HandleFunc("POST /api/setup/interview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message string `json:"message"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		state, err := a.InterviewIdentity(ctx, in.Message)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, state)
	})
	mux.HandleFunc("POST /api/setup/apply", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			RecommendationID string `json:"recommendation_id"`
			Accepted         bool   `json:"accepted"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		identity, err := a.ApplyIdentity(r.Context(), in.RecommendationID, in.Accepted)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, identity)
	})

	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message string `json:"message"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		v, err := a.Chat(ctx, in.Message)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var in core.ProjectInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.CreateProject(r.Context(), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
	})
	mux.HandleFunc("POST /api/decisions/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Choice string `json:"choice"`
			Answer string `json:"answer"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if (strings.TrimSpace(in.Choice) == "") == (strings.TrimSpace(in.Answer) == "") {
			problem(w, errors.New("provide one selected choice or custom answer"))
			return
		}
		answer := in.Choice
		if strings.TrimSpace(in.Answer) != "" {
			answer = in.Answer
		}
		v, err := a.Core.ResolveDecision(r.Context(), r.PathValue("id"), answer)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/decisions/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string `json:"reason"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.DismissDecision(r.Context(), r.PathValue("id"), in.Reason)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/memories", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Content string `json:"content"`
			Key     string `json:"key,omitempty"`
			Kind    string `json:"kind,omitempty"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if in.Key == "" {
			in.Key = in.Content
		}
		v, err := a.Core.RememberKind(r.Context(), in.Key, in.Content, in.Kind, "owner")
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
	})
	// Correcting is deliberately its own route: recording and correcting are
	// different acts, and the original memory is kept either way.
	mux.HandleFunc("POST /api/memories/{id}/correct", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Content string `json:"content"`
			Kind    string `json:"kind,omitempty"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.Correct(r.Context(), r.PathValue("id"), in.Content, in.Kind)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
	})
	mux.HandleFunc("DELETE /api/memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Core.Forget(r.Context(), r.PathValue("id")); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]bool{"deleted": true})
	})
	mux.HandleFunc("POST /api/control", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool `json:"paused"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if err := a.Core.SetPaused(r.Context(), in.Paused); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, in)
	})
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := a.SyncLinear(r.Context()); err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]bool{"synced": true})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "unknown API endpoint") })
	assets, err := fs.Sub(dashboard.Assets, "assets")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			if strings.Contains(path, ".") {
				http.NotFound(w, r)
				return
			}
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
	return auth.Middleware(mux)
}
func problem(w http.ResponseWriter, err error) {
	status := 400
	if errors.Is(err, core.ErrChatQueueFull) {
		status = http.StatusTooManyRequests
	}
	if errors.Is(err, core.ErrNotFound) {
		status = 404
	}
	if errors.Is(err, core.ErrConflict) {
		status = 409
	}
	fail(w, status, err.Error())
}
