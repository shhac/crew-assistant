package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

func registerAgentControls(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/agents/{id}/conversation", func(w http.ResponseWriter, r *http.Request) {
		cursor := func(name string) (int64, error) {
			v := r.URL.Query().Get(name)
			if v == "" {
				return 0, nil
			}
			return strconv.ParseInt(v, 10, 64)
		}
		after, err := cursor("after")
		if err != nil {
			fail(w, 400, "invalid after cursor")
			return
		}
		before, err := cursor("before")
		if err != nil {
			fail(w, 400, "invalid before cursor")
			return
		}
		limit, err := cursor("limit")
		if err != nil || limit < 0 || limit > 100 {
			fail(w, 400, "limit must be between 1 and 100")
			return
		}
		page, err := a.Core.AgentConversation(r.Context(), r.PathValue("id"), after, before, int(limit))
		if err != nil {
			if errors.Is(err, worker.ErrUncertain) {
				fail(w, 503, "Worker operation requires reconciliation; retain the same operation ID")
			} else {
				problem(w, err)
			}
			return
		}
		controls, err := a.AgentControls(r.Context(), r.PathValue("id"))
		if err != nil {
			if errors.Is(err, worker.ErrUncertain) {
				fail(w, 503, "Worker operation requires reconciliation; retain the same operation ID")
			} else {
				problem(w, err)
			}
			return
		}
		respond(w, 200, struct {
			core.AgentConversationPage
			Controls app.AgentControls `json:"controls"`
		}{page, controls})
	})
	mux.HandleFunc("POST /api/agents/{id}/control", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Action      string `json:"action"`
			OperationID string `json:"operation_id"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		out, err := a.ControlAgent(r.Context(), r.PathValue("id"), in.Action, in.OperationID)
		if err != nil {
			if errors.Is(err, worker.ErrUncertain) {
				fail(w, 503, "Worker operation requires reconciliation; retain the same operation ID")
			} else {
				problem(w, err)
			}
			return
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("POST /api/agents/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MessageID string `json:"message_id"`
			Message   string `json:"message"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		out, err := a.SteerAgent(r.Context(), r.PathValue("id"), in.MessageID, in.Message)
		if err != nil {
			if errors.Is(err, worker.ErrUncertain) {
				fail(w, 503, "Worker operation requires reconciliation; retain the same operation ID")
			} else {
				problem(w, err)
			}
			return
		}
		respond(w, 201, out)
	})
}
