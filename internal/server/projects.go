package server

import (
	"net/http"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerProjectWork serves the owner's direct controls over a project: its
// brief, its team, the outcomes asked of it, and what those produced.
func registerProjectWork(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("PUT /api/projects/{id}/brief", func(w http.ResponseWriter, r *http.Request) {
		var in core.BriefInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.UpdateBrief(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/prefix", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prefix string `json:"prefix"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.SetProjectPrefix(r.Context(), r.PathValue("id"), in.Prefix)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team", func(w http.ResponseWriter, r *http.Request) {
		var in work.TeamChoice
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetTeam(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/team/{kind}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Member string `json:"member"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetSeat(r.Context(), r.PathValue("id"), r.PathValue("kind"), in.Member)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/team/seats", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Seat string `json:"seat"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.AddSeat(r.Context(), r.PathValue("id"), in.Seat)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/team/seats/{seat}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.RemoveSeat(r.Context(), r.PathValue("id"), r.PathValue("seat"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/parallel", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MaxActive int `json:"max_active"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetParallel(r.Context(), r.PathValue("id"), in.MaxActive)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/workspace", func(w http.ResponseWriter, r *http.Request) {
		var in work.Workspace
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetWorkspace(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/landing", func(w http.ResponseWriter, r *http.Request) {
		var in core.LandPolicy
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.SetLanding(r.Context(), r.PathValue("id"), in)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/land", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.LandTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks", func(w http.ResponseWriter, r *http.Request) {
		var in core.TaskInput
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Work.QueueTask(r.Context(), r.PathValue("id"), in, core.LinkedByOwner)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/place", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.Place(r.Context(), r.PathValue("id"), r.PathValue("task"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
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
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
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
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{task}/links/{other}", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.UnlinkTasks(r.Context(), core.Link{Project: r.PathValue("id"), Task: r.PathValue("task"), Other: r.PathValue("other"), By: core.LinkedByOwner})
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
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
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
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
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{id}/tasks/order", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TaskIDs []string `json:"task_ids"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		v, err := a.Core.OrderTasks(r.Context(), r.PathValue("id"), in.TaskIDs, core.OrderedByOwner)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
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
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 201, v)
	})
	// What one seat has done on a task, for its member's panel. Prompts and
	// tool payloads are private: they are served to the signed-in owner here
	// and nowhere else, and are never part of the polled state.
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/seats/{seat}/steps", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Core.TurnSteps(r.Context(), r.PathValue("id"), r.PathValue("task"), r.PathValue("seat"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]any{"steps": v})
	})
	mux.HandleFunc("POST /api/projects/{id}/tasks/{task}/stop", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Work.StopTask(r.Context(), r.PathValue("id"), r.PathValue("task"))
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/revisions/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 {
			fail(w, 400, "invalid revision")
			return
		}
		files, err := a.Work.RevisionPreview(r.Context(), r.PathValue("id"), r.PathValue("task"), n)
		if err != nil {
			problem(w, err)
			return
		}
		respond(w, 200, map[string]any{"files": files})
	})
}
