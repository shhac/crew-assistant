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
	registerPMChat(mux, a)
	registerProjectWork(mux, a)
	registerProjectTasks(mux, a)
	registerMembers(mux, a)
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		s, err := a.Snapshot(r.Context())
		reply(w, 200, struct {
			core.Snapshot
			Demo bool `json:"demo"`
		}{s, a.Demo}, err)
	})
	// Read-only and bounded: the logins are inspected, never used or changed.
	mux.HandleFunc("GET /api/usage", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.Usage(r.Context())) })
	mux.HandleFunc("POST /api/operations/{id}/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Note string `json:"note"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		reply(w, 200, map[string]bool{"acknowledged": true}, a.Core.AcknowledgeEvent(r.Context(), r.PathValue("id"), in.Note))
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, a.Config()) })
	mux.HandleFunc("GET /api/config/defaults", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, configDefaults()) })
	mux.HandleFunc("GET /api/providers", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, providerChoices(a.Config())) })
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var c config.Config
		if decodeConfig(w, r, &c) != nil {
			return
		}
		reply(w, 200, c, a.UpdateConfig(c))
	})
	mux.HandleFunc("GET /api/connection-profiles", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.DiscoverConnectionProfiles(r.Context(), r.URL.Query().Get("tool"))
		reply(w, 200, result, err)
	})
	// Suggestions for a new assistant or a new member: subject is assistant
	// or member.
	mux.HandleFunc("GET /api/setup/{subject}", func(w http.ResponseWriter, r *http.Request) {
		state, err := a.IdentitySetup(r.PathValue("subject"))
		reply(w, 200, state, err)
	})
	mux.HandleFunc("POST /api/setup/{subject}/interview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Message string `json:"message"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		state, err := a.InterviewIdentity(ctx, r.PathValue("subject"), in.Message)
		reply(w, 200, state, err)
	})
	mux.HandleFunc("DELETE /api/setup/{subject}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"reset": true}, a.ResetIdentitySetup(r.PathValue("subject")))
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
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var in core.ProjectInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.CreateProject(r.Context(), in)
		reply(w, 201, v, err)
	})
	mux.HandleFunc("POST /api/decisions/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Choice string `json:"choice"`
			Answer string `json:"answer"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.ResolveDecision(r.Context(), r.PathValue("id"), in.Choice, in.Answer)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/decisions/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Reason string `json:"reason"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.DismissDecision(r.Context(), r.PathValue("id"), in.Reason)
		reply(w, 200, v, err)
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
		reply(w, 201, v, err)
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
		reply(w, 201, v, err)
	})
	mux.HandleFunc("DELETE /api/memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"deleted": true}, a.Core.Forget(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/control", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool `json:"paused"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		reply(w, 200, in, a.Core.SetPaused(r.Context(), in.Paused))
	})
	mux.HandleFunc("POST /api/sync", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"synced": true}, a.SyncLinear(r.Context()))
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
	if errors.Is(err, app.ErrStopping) {
		status = http.StatusServiceUnavailable
	}
	fail(w, status, ownerText(err))
}

func reply(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		problem(w, err)
		return
	}
	respond(w, status, v)
}

// ownerText is an error as the owner reads it: without the sentinel that
// classified it, and starting with a capital letter.
func ownerText(err error) string {
	text := core.Reason(err)
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
