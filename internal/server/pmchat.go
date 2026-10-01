package server

import (
	"net/http"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
)

func registerPMChat(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/projects/{id}/pm-chat", func(w http.ResponseWriter, r *http.Request) {
		messages, err := a.Core.PMChat(r.Context(), r.PathValue("id"))
		var avatarSVG string
		if err == nil {
			snap, snapErr := a.Core.Snapshot(r.Context())
			if snapErr != nil {
				err = snapErr
			} else if p, ok := snap.Project(r.PathValue("id")); ok {
				if seat, ok := p.PMSeat(); ok {
					// Display only: reuse the preset without creating a member.
					avatarSVG, err = config.DefaultAvatar(seat.Name).SVG()
				}
			}
		}
		reply(w, 200, map[string]any{"messages": messages, "avatar_svg": avatarSVG}, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/pm-chat/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		m, err := a.Work.SendPMChat(r.Context(), r.PathValue("id"), in.ID, in.Text)
		m.Token = ""
		reply(w, 201, m, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/pm-chat/messages/{msg}/retry", func(w http.ResponseWriter, r *http.Request) {
		m, err := a.Work.RetryPMChat(r.Context(), r.PathValue("id"), r.PathValue("msg"))
		m.Token = ""
		reply(w, 200, m, err)
	})
}
