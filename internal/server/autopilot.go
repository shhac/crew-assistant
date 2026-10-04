package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/autopilot"
	"github.com/shhac/crew-assistant/internal/core"
)

// These routes run behind New's existing owner authentication and mutation
// admission middleware. No request can register a function or select an actor.
func registerAutopilot(mux *http.ServeMux, a *app.App) {
	mux.HandleFunc("GET /api/autopilot/summary", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(w, 400, "invalid summary query")
			return
		}
		for key, values := range q {
			if len(values) != 1 || (key != "after" && key != "project_id") {
				fail(w, 400, "invalid summary filter")
				return
			}
		}
		var after int64
		if _, ok := q["after"]; ok {
			after, err = autopilotInt(q.Get("after"))
		} else {
			after, err = a.Autopilot.Progress(r.Context(), "owner_seen")
		}
		if err != nil {
			if _, ok := q["after"]; ok {
				fail(w, 400, "invalid after")
			} else {
				fail(w, 500, "Unable to read summary")
			}
			return
		}
		out, err := a.Autopilot.Summary(r.Context(), core.SummaryQuery{After: after, ProjectID: q.Get("project_id")})
		autopilotReadReply(w, out, err)
	})
	mux.HandleFunc("POST /api/autopilot/summary/ack", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Boundary  *int64 `json:"boundary"`
			ProjectID string `json:"project_id,omitempty"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if in.ProjectID != "" {
			fail(w, 400, "Only unfiltered summaries may be acknowledged")
			return
		}
		if in.Boundary == nil {
			fail(w, 400, "boundary required")
			return
		}
		err := a.Autopilot.AcknowledgeSummary(r.Context(), *in.Boundary)
		autopilotReadReply(w, map[string]bool{"acknowledged": err == nil}, err)
	})
	mux.HandleFunc("PUT /api/autopilot/digest", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled  *bool   `json:"enabled"`
			At       string  `json:"at"`
			Revision *uint64 `json:"revision"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		if in.Enabled == nil || in.Revision == nil {
			fail(w, 400, "enabled and revision required")
			return
		}
		digest := autopilot.DailyDigest{Enabled: *in.Enabled, At: in.At}
		if err := digest.Validate(); err != nil {
			fail(w, 400, err.Error())
			return
		}
		out, err := a.SetAutopilotDigest(digest, *in.Revision)
		reply(w, 200, out.Autopilot.DailyDigest, err)
	})
	mux.HandleFunc("GET /api/autopilot/digests", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(w, 400, "invalid digest query")
			return
		}
		for key, values := range q {
			if len(values) != 1 || (key != "before" && key != "limit") {
				fail(w, 400, "invalid digest filter")
				return
			}
		}
		limit, err := autopilotInt(q.Get("limit"))
		if err != nil {
			fail(w, 400, "invalid limit")
			return
		}
		digests, err := a.Autopilot.Digests(r.Context(), q.Get("before"), int(limit))
		if err != nil {
			autopilotReadReply(w, nil, err)
			return
		}
		type item struct {
			core.AutopilotDigest
			Summary core.AutopilotSummary `json:"summary"`
		}
		out := []item{}
		for _, d := range digests {
			summary, err := a.Autopilot.Summary(r.Context(), core.SummaryQuery{After: d.From, Boundary: d.Boundary, FixedBoundary: true})
			if err != nil {
				autopilotReadReply(w, nil, err)
				return
			}
			out = append(out, item{d, summary})
		}
		respond(w, 200, out)
	})

	mux.HandleFunc("GET /api/autopilot", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, struct {
			Functions []autopilot.Function `json:"functions"`
			Settings  autopilot.Settings   `json:"settings"`
		}{a.Autopilot.Catalog(), a.Config().Autopilot})
	})
	mux.HandleFunc("PUT /api/autopilot/modes/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Mode     autopilot.Mode `json:"mode"`
			Revision uint64         `json:"revision"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		cfg, err := a.SetAutopilotMode(r.PathValue("id"), in.Mode, in.Revision)
		reply(w, 200, cfg.Autopilot, err)
	})
	mux.HandleFunc("PUT /api/projects/{id}/operator-permission", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Allowed  bool   `json:"allowed"`
			Revision uint64 `json:"revision"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		p, err := a.Core.SetOperatorPermission(r.Context(), r.PathValue("id"), in.Allowed, in.Revision)
		reply(w, 200, p, err)
	})
	mux.HandleFunc("GET /api/autopilot/actions/{id}", func(w http.ResponseWriter, r *http.Request) {
		out, err := a.Autopilot.Action(r.Context(), r.PathValue("id"))
		autopilotReadReply(w, out, err)
	})
	mux.HandleFunc("POST /api/autopilot/actions/{id}/{verb}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Revision    uint64               `json:"revision"`
			Replacement *core.ConcreteAction `json:"replacement,omitempty"`
		}
		if decode(w, r, &in) != nil {
			return
		}
		verb := r.PathValue("verb")
		if verb != "override" && in.Replacement != nil {
			fail(w, 400, "only override accepts replacement arguments")
			return
		}
		if verb == "reconcile" {
			out, err := a.Autopilot.Reconcile(r.Context(), r.PathValue("id"), in.Revision)
			reply(w, 200, out, err)
			return
		}
		out, err := a.Autopilot.OwnerAction(r.Context(), r.PathValue("id"), in.Revision, verb, in.Replacement)
		reply(w, 200, out, err)
	})
	mux.HandleFunc("GET /api/autopilot/pending", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(w, 400, "invalid pending query")
			return
		}
		for key, values := range q {
			if (key != "after" && key != "limit") || len(values) != 1 {
				fail(w, 400, "invalid pending filter")
				return
			}
		}
		limit, err := autopilotInt(q.Get("limit"))
		if err != nil {
			fail(w, 400, "invalid limit")
			return
		}
		out, err := a.Autopilot.Pending(r.Context(), q.Get("after"), int(limit))
		autopilotReadReply(w, out, err)
	})
	mux.HandleFunc("GET /api/autopilot/history", func(w http.ResponseWriter, r *http.Request) {
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(w, 400, "invalid history query")
			return
		}
		for key, values := range q {
			if len(values) != 1 || (key != "project_id" && key != "task_id" && key != "cursor" && key != "limit" && key != "forward" && key != "after") {
				fail(w, 400, "unknown history filter")
				return
			}
		}
		limit, err := autopilotInt(q.Get("limit"))
		if err != nil {
			fail(w, 400, "invalid limit")
			return
		}
		after, err := autopilotInt(q.Get("after"))
		if err != nil {
			fail(w, 400, "invalid after")
			return
		}
		forward := false
		if q.Get("forward") != "" {
			forward, err = strconv.ParseBool(q.Get("forward"))
			if err != nil {
				fail(w, 400, "invalid forward")
				return
			}
		}
		out, err := a.Autopilot.History(r.Context(), core.AutopilotHistoryQuery{ProjectID: q.Get("project_id"), TaskID: q.Get("task_id"), Cursor: q.Get("cursor"), Limit: int(limit), Forward: forward, After: after})
		autopilotReadReply(w, out, err)
	})
}

func autopilotReadReply(w http.ResponseWriter, out any, err error) {
	if err == nil {
		respond(w, 200, out)
	} else if errors.Is(err, core.ErrAutopilotPage) {
		fail(w, 400, "invalid autopilot pagination")
	} else if errors.Is(err, core.ErrNotFound) {
		problem(w, err)
	} else {
		fail(w, 500, "Unable to read autopilot history")
	}
}

func autopilotInt(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
