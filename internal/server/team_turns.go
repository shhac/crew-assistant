package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/session"
)

// Explicit projections keep recording authority and launch details private.
type turnUsage struct {
	Known      bool   `json:"known"`
	CacheKnown bool   `json:"cache_known"`
	Final      bool   `json:"final"`
	Status     string `json:"status"`
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cache_read"`
	CacheWrite *int64 `json:"cache_write"`
}

func usageView(u session.Usage, observation bool) turnUsage {
	v := turnUsage{Known: u.Known, CacheKnown: u.CacheKnown, Final: u.Final, Status: "unknown"}
	if u.Known {
		v.Input, v.Output = &u.Input, &u.Output
	}
	if u.CacheKnown {
		v.CacheRead, v.CacheWrite = &u.CacheRead, &u.CacheWrite
	}
	if u.Known || u.CacheKnown {
		v.Status = "partial"
		if u.Final && !observation {
			v.Status = "final"
		}
	}
	return v
}

type turnTerminal struct {
	At                 time.Time `json:"at"`
	Outcome            string    `json:"outcome"`
	FailureStage       string    `json:"failure_stage,omitempty"`
	ProviderStatus     string    `json:"provider_status,omitempty"`
	ProviderTurnID     string    `json:"provider_turn_id,omitempty"`
	NativeError        bool      `json:"native_error"`
	CleanupConfirmed   bool      `json:"cleanup_confirmed"`
	Usage              turnUsage `json:"usage"`
	Observed           turnUsage `json:"observed"`
	CompactionUsage    turnUsage `json:"compaction_usage"`
	CompactionObserved turnUsage `json:"compaction_observed"`
}

type turnView struct {
	ID                 string                `json:"id"`
	ProjectID          string                `json:"project_id"`
	TaskID             string                `json:"task_id,omitempty"`
	MemberID           string                `json:"member_id,omitempty"`
	MemberName         string                `json:"member_name,omitempty"`
	Role               string                `json:"role"`
	Seat               string                `json:"seat"`
	Engine             string                `json:"engine"`
	Model              string                `json:"model"`
	ProviderDefault    bool                  `json:"provider_default"`
	PreviousID         string                `json:"previous_id,omitempty"`
	RetryCause         string                `json:"retry_cause,omitempty"`
	AdmittedAt         time.Time             `json:"admitted_at"`
	Opening            *core.TeamTurnOpening `json:"opening"`
	AcceptedAt         *time.Time            `json:"accepted_at"`
	Terminal           *turnTerminal         `json:"terminal"`
	CleanupConfirmedAt *time.Time            `json:"cleanup_confirmed_at"`
	Held               bool                  `json:"held"`
	Lifecycle          string                `json:"lifecycle"`
}

func teamTurnView(t core.TeamTurn) turnView {
	v := turnView{ID: t.ID, ProjectID: t.ProjectID, TaskID: t.TaskID, MemberID: t.MemberID, MemberName: t.MemberName, Role: t.Role, Seat: t.Seat, Engine: t.Engine, Model: t.Model, ProviderDefault: t.Model == "", PreviousID: t.PreviousID, RetryCause: t.RetryCause, AdmittedAt: t.AdmittedAt, Opening: t.Opening, AcceptedAt: t.AcceptedAt, CleanupConfirmedAt: t.CleanupConfirmedAt, Held: t.Held, Lifecycle: "admitted"}
	if t.Opening != nil {
		v.Lifecycle = "opened"
	}
	if t.AcceptedAt != nil {
		v.Lifecycle = "accepted"
	}
	if x := t.Terminal; x != nil {
		v.Lifecycle = "terminal"
		v.Terminal = &turnTerminal{At: x.At, Outcome: x.Outcome, FailureStage: x.FailureStage, ProviderStatus: x.ProviderStatus, ProviderTurnID: x.ProviderTurnID, NativeError: x.NativeError, CleanupConfirmed: x.CleanupConfirmed, Usage: usageView(x.Usage, false), Observed: usageView(x.Observed, true), CompactionUsage: usageView(x.CompactionUsage, false), CompactionObserved: usageView(x.CompactionObserved, true)}
	}
	return v
}

type turnHistoryResponse struct {
	Turns      []turnView             `json:"turns"`
	NextBefore string                 `json:"next_before,omitempty"`
	Aggregate  core.TeamTurnAggregate `json:"aggregate"`
}

func registerTeamTurns(mux *http.ServeMux, a *app.App) {
	handler := func(member bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			snap, err := a.Core.Snapshot(r.Context())
			if err != nil {
				fail(w, 500, "Unable to read team-turn history")
				return
			}
			filter := core.TeamTurnFilter{}
			if member {
				m, ok := snap.Member(r.PathValue("id"))
				if !ok {
					problem(w, core.ErrNotFound)
					return
				}
				filter.MemberID = m.ID
			} else {
				p, ok := snap.Project(r.PathValue("id"))
				t, exists := snap.FindTask(r.PathValue("task"))
				if !ok || !exists || t.ProjectID != p.ID {
					problem(w, core.ErrNotFound)
					return
				}
				filter.ProjectID, filter.TaskID = p.ID, t.ID
			}
			limit := 50
			q, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil {
				problem(w, core.ErrTeamTurnPage)
				return
			}
			if values, ok := q["limit"]; ok {
				if len(values) != 1 {
					problem(w, core.ErrTeamTurnPage)
					return
				}
				limit, err = strconv.Atoi(values[0])
				if err != nil || limit < 1 || limit > 200 {
					problem(w, core.ErrTeamTurnPage)
					return
				}
			}
			if values, ok := q["before"]; ok && (len(values) != 1 || values[0] == "") {
				problem(w, core.ErrTeamTurnPage)
				return
			}
			h, err := a.Core.TeamTurnHistory(r.Context(), filter, limit, q.Get("before"))
			if err != nil {
				if errors.Is(err, core.ErrTeamTurnPage) {
					problem(w, err)
				} else {
					fail(w, 500, "Unable to read team-turn history")
				}
				return
			}
			response := turnHistoryResponse{Turns: []turnView{}, NextBefore: h.NextBefore, Aggregate: h.Aggregate}
			for _, t := range h.Turns {
				response.Turns = append(response.Turns, teamTurnView(t))
			}
			respond(w, 200, response)
		}
	}
	mux.HandleFunc("GET /api/projects/{id}/tasks/{task}/team-turns", handler(false))
	mux.HandleFunc("GET /api/members/{id}/team-turns", handler(true))
}
