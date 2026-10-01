package server

import (
	"net/http"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
)

// registerProjectTasks serves the outcomes the owner asks of a project, and
// what those produced.
func registerProjectTasks(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/linear", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ConnectionID string `json:"connection_id"`
			Profile      string `json:"profile"`
			Kind         string `json:"kind"`
			Ref          string `json:"ref"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.LinkTaskLinear(r.Context(), r.PathValue("id"), r.PathValue("task"), in.ConnectionID, in.Profile, in.Kind, in.Ref, core.LinkedByOwner)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task}/linear/{kind}/{ref}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.UnlinkTaskLinear(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("kind"), r.PathValue("ref"), core.LinkedByOwner)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/blockers", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Kind        string `json:"kind"`
			Description string `json:"description"`
			Task        string `json:"task"`
			LandingOnly bool   `json:"landing_only"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetBlocker(r.Context(), core.BlockerInput{Project: r.PathValue("id"), Task: r.PathValue("task"), Kind: in.Kind, Description: in.Description, Other: in.Task, LandingOnly: in.LandingOnly, By: core.LinkedByOwner})
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task}/blockers/{blocker}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.ClearBlocker(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("blocker"), core.LinkedByOwner, "cleared by the owner")
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/land", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.LandTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		var in core.TaskInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.QueueTask(r.Context(), r.PathValue("id"), in, core.LinkedByOwner)
		reply(w, 201, v, err)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/place", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.Place(r.Context(), r.PathValue("id"), r.PathValue("task"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/drafts", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Ref     string `json:"ref"`
			Note    string `json:"note"`
			Approve bool   `json:"approve"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.AdoptDraft(r.Context(), r.PathValue("id"), r.PathValue("task"), in.Ref, in.Note, in.Approve)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/links", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Relation string `json:"relation"`
			Task     string `json:"task"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.LinkTasks(r.Context(), core.Link{Project: r.PathValue("id"), Task: r.PathValue("task"), Relation: in.Relation, Other: in.Task, By: core.LinkedByOwner})
		reply(w, 200, v, err)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task}/links/{other}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.UnlinkTasks(r.Context(), core.Link{Project: r.PathValue("id"), Task: r.PathValue("task"), Other: r.PathValue("other"), By: core.LinkedByOwner})
		reply(w, 200, v, err)
	})
	// A note is JSON text, or a multipart form with its text and files, kept
	// in one change.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/notes", func(w http.ResponseWriter, r *http.Request) {
		in := core.NoteInput{Project: r.PathValue("id"), Task: r.PathValue("task"), By: core.FromOwner, Kind: core.FromOwner}
		if isMultipart(r) {
			var err error
			if in.Text, in.Files, err = noteForm(w, r); err != nil {
				return
			}
		} else {
			var body struct {
				Text string `json:"text"`
			}
			if decode(w, r, &body) != nil {
				return
			}
			in.Text = body.Text
		}
		v, err := a.Core.AddNote(r.Context(), in)
		reply(w, 201, v, err)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/attachments/{att}", func(w http.ResponseWriter, r *http.Request) {
		att, path, err := a.Core.OpenAttachment(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("att"))
		if err != nil {
			problem(w, err)
			return
		}
		serveAttachment(w, r, att, path)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/edits/{edit}/undo", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.UndoTaskEdit(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("edit"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/tasks/order", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TaskIDs []string `json:"task_ids"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.OrderTasks(r.Context(), r.PathValue("id"), in.TaskIDs, core.OrderedByOwner)
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			To   string `json:"to"`
			Text string `json:"text"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.MessageTeam(r.Context(), r.PathValue("id"), r.PathValue("task"), in.To, core.FromOwner, in.Text)
		reply(w, 201, v, err)
	})
	// What one seat has done on a task, for its member's panel. Prompts and
	// tool payloads are private: they are served to the signed-in owner here
	// and nowhere else, and are never part of the polled state.
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/seats/{seat}/steps", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.TurnSteps(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("seat"))
		reply(w, 200, map[string]any{"steps": v}, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/team", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.UseProjectTeam(r.Context(), r.PathValue("id"), r.PathValue("task"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/stop", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.StopTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		reply(w, 200, v, err)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/revisions/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 {
			fail(w, 400, "invalid revision")
			return
		}
		files, err := a.Work.RevisionPreview(r.Context(), r.PathValue("id"), r.PathValue("task"), n)
		reply(w, 200, map[string]any{"files": files}, err)
	})
}
