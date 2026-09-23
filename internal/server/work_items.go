package server

import (
	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"net/http"
)

func registerWorkItems(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("DELETE /api/work-items/{id}/queue", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.Core.CancelQueuedWorkItem(r.Context(), r.PathValue("id"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /api/projects/{id}/work-items/queue", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title              string `json:"title"`
			Objective          string `json:"objective"`
			AcceptanceCriteria string `json:"acceptance_criteria"`
			AfterWorkItemID    string `json:"after_work_item_id"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.Core.QueueWorkItem(r.Context(), core.WorkItemInput{ProjectID: r.PathValue("id"), Title: in.Title, Objective: in.Objective, AcceptanceCriteria: in.AcceptanceCriteria, AfterWorkItemID: in.AfterWorkItemID})
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /api/projects/{id}/work-items", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title              string `json:"title"`
			Objective          string `json:"objective"`
			AcceptanceCriteria string `json:"acceptance_criteria"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.Core.CreateWorkItem(r.Context(), core.WorkItemInput{ProjectID: r.PathValue("id"), Title: in.Title, Objective: in.Objective, AcceptanceCriteria: in.AcceptanceCriteria})
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /api/work-items/{id}/steering", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MessageID string `json:"message_id"`
			Message   string `json:"message"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.AddWorkItemSteering(r.Context(), r.PathValue("id"), in.MessageID, in.Message)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusCreated, result)
	})
	mux.HandleFunc("POST /api/work-items/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ReviewRevision string   `json:"review_revision"`
			Evidence       []string `json:"evidence"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		result, err := a.Core.AcceptWorkItem(r.Context(), r.PathValue("id"), in.ReviewRevision, in.Evidence, "owner")
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, http.StatusOK, result)
	})
}
