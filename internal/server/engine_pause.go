package server

import (
	"net/http"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
)

func registerEnginePauses(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/engines", func(w http.ResponseWriter, r *http.Request) {
		states, err := a.Work.EngineStatus(r.Context())
		reply(w, 200, states, err)
	})
	mux.HandleFunc("PUT /api/engines/{engine}/paused", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool       `json:"paused"`
			Until  *time.Time `json:"until,omitempty"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		var err error
		if in.Paused {
			err = a.Work.PauseEngine(r.Context(), r.PathValue("engine"), in.Until)
		} else {
			err = a.Work.ResumeEngine(r.Context(), r.PathValue("engine"))
		}
		if err != nil {
			reply(w, 200, nil, err)
			return
		}
		states, err := a.Work.EngineStatus(r.Context())
		for _, state := range states {
			if state.Engine == r.PathValue("engine") {
				reply(w, 200, state, err)
				return
			}
		}
		reply(w, 200, nil, err)
	})
}
