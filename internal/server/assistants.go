package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
)

// registerAssistants serves the owner's assistant profiles, kept in the
// config. Who sits in the seat is chosen in Settings, through the config.
func registerAssistants(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("POST /api/assistants", func(w http.ResponseWriter, r *http.Request) {
		var in app.AssistantInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.CreateAssistant(r.Context(), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/assistants/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in app.AssistantInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.SaveAssistant(r.Context(), r.PathValue("id"), in)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/assistants/{id}", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]bool{"deleted": true}, a.DeleteAssistant(r.Context(), r.PathValue("id")))
	})
	mux.HandleFunc("POST /api/assistants/{id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Look string `json:"look"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		reply(w, 202, map[string]bool{"drawing": true}, a.DrawAssistant(r.Context(), r.PathValue("id"), in.Look))
	})
}
