package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
)

func registerWorkerSetup(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("POST /api/projects/{id}/worker", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Workspace string `json:"workspace"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		result, err := a.PrepareWorker(ctx, r.PathValue("id"), in.Workspace)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, result)
	})
	mux.HandleFunc("POST /api/projects/{id}/coordinate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Next string `json:"next"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if len(in.Next) > 12000 {
			fail(w, 400, "describe the next outcome in at most 12000 characters")
			return
		}
		s, err := a.Snapshot(r.Context())
		if err != nil {
			problem(w, err)
			return
		}
		for _, p := range s.Projects {
			if p.ID != r.PathValue("id") {
				continue
			}
			title := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(p.Title)
			message := "Let's work on [" + title + "](#/projects/" + p.ID + "). "
			if strings.TrimSpace(in.Next) == "" {
				message += "Help me decide what to do next. Ask about the outcome before preparing or starting project work."
			} else {
				message += "What I want to do next: " + strings.TrimSpace(in.Next)
			}
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
			defer cancel()
			result, err := a.Chat(ctx, message)
			if err != nil {
				problem(w, err)
				return
			}
			respond(w, 200, result)
			return
		}
		problem(w, core.ErrNotFound)
	})
}
