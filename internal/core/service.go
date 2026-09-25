package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shhac/crew-assistant/internal/config"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("state conflict")

// Service deliberately exposes no execution, shell, production or purchase capability.
type Service struct {
	store *Store
	mu    sync.RWMutex
	cfg   config.Config
	now   func() time.Time
}

func NewService(store *Store, cfg config.Config) *Service {
	return &Service{store: store, cfg: cfg, now: time.Now}
}
func (s *Service) UpdateConfig(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}
func (s *Service) configuration() config.Config { s.mu.RLock(); defer s.mu.RUnlock(); return s.cfg }
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	v, err := s.store.Snapshot(ctx)
	cfg := s.configuration()
	// A loaded configuration is valid, so its avatar always draws.
	svg, _ := cfg.Assistant.Avatar.SVG()
	v.Assistant = Assistant{Name: cfg.Assistant.Name, Personality: cfg.Assistant.Personality, Theme: cfg.Assistant.Theme, Avatar: cfg.Assistant.Avatar, AvatarSVG: svg}
	for i := range v.Members {
		v.Members[i].AvatarSVG, _ = v.Members[i].Avatar.SVG()
	}
	v.PendingOperations = []PendingOperation{}
	for id, done := range v.Events {
		if !done {
			v.PendingOperations = append(v.PendingOperations, pendingOperation(v, id))
		}
	}
	sort.Slice(v.PendingOperations, func(i, j int) bool { return v.PendingOperations[i].ID < v.PendingOperations[j].ID })
	sortActivity(v.Activity)
	return v, err
}

// sortActivity presents the feed newest first. Recorded times can come from
// more than one clock, so append order is not a reliable proxy for recency;
// the identifier only breaks ties deterministically.
func sortActivity(entries []Activity) {
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		}
		return entries[i].ID > entries[j].ID
	})
}
func uid() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func record(v *Snapshot, now time.Time, project, kind, summary string) {
	v.Activity = append(v.Activity, Activity{ID: uid(), ProjectID: project, Kind: kind, Summary: summary, CreatedAt: now})
}
func project(v *Snapshot, id string) *Project {
	for i := range v.Projects {
		if v.Projects[i].ID == id {
			return &v.Projects[i]
		}
	}
	return nil
}
func required(fields ...string) bool {
	for _, f := range fields {
		if strings.TrimSpace(f) == "" {
			return false
		}
	}
	return true
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func (s *Service) CreateDecision(ctx context.Context, in DecisionInput) (Decision, error) {
	return s.openDecision(ctx, DecisionChoice, in)
}

func (s *Service) openDecision(ctx context.Context, kind string, in DecisionInput) (Decision, error) {
	if !required(in.Title, in.Context, in.Recommendation) || len(in.Choices) < 2 {
		return Decision{}, errors.New("decision requires title, context, recommendation and at least two choices")
	}
	for _, c := range in.Choices {
		if !required(c) {
			return Decision{}, errors.New("decision choices cannot be blank")
		}
	}
	out := Decision{ID: uid(), Kind: kind, ProjectID: in.ProjectID, Title: in.Title, Context: in.Context, Recommendation: in.Recommendation, Choices: in.Choices, Status: DecisionOpen, CreatedAt: s.now().UTC()}
	err := s.store.update(ctx, func(v *Snapshot) error {
		if in.ProjectID != "" && project(v, in.ProjectID) == nil {
			return ErrNotFound
		}
		v.Decisions = append(v.Decisions, out)
		record(v, out.CreatedAt, in.ProjectID, "decision.opened", in.Title)
		return nil
	})
	return out, err
}
func (s *Service) AddMessage(ctx context.Context, role, text string) (Message, error) {
	if !contains([]string{"user", "assistant", "system"}, role) || !required(text) {
		return Message{}, errors.New("message requires a supported role and content")
	}
	out := Message{ID: uid(), Role: role, Content: text, CreatedAt: s.now().UTC()}
	err := s.store.update(ctx, func(v *Snapshot) error { v.Messages = append(v.Messages, out); return nil })
	return out, err
}
func (s *Service) SetPaused(ctx context.Context, paused bool) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		v.Paused = paused
		record(v, s.now().UTC(), "", "coordination.paused", fmt.Sprintf("Paused: %t", paused))
		return nil
	})
}

// ModelKey names one engine's model for ModelWindows.
func ModelKey(engine, model string) string { return engine + "/" + model }

// ModelWindow is the context window, in tokens, the provider last stated for
// an engine's model, or 0 when it has not said.
func (s Snapshot) ModelWindow(engine, model string) int {
	return s.ModelWindows[ModelKey(engine, model)]
}

// RecordModelWindow keeps the context window a provider stated for a model.
// Windows change when a model does, so the latest statement stands.
func (s *Service) RecordModelWindow(ctx context.Context, engine, model string, tokens int) error {
	if tokens <= 0 || engine == "" || model == "" {
		return nil
	}
	if snap, err := s.store.Snapshot(ctx); err == nil && snap.ModelWindow(engine, model) == tokens {
		return nil
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		if v.ModelWindows == nil {
			v.ModelWindows = map[string]int{}
		}
		v.ModelWindows[ModelKey(engine, model)] = tokens
		return nil
	})
}

// ReserveModelCall is persisted before inference and is deliberately never
// refunded on ambiguous errors. The quota window is a UTC calendar day.
func (s *Service) ReserveModelCall(ctx context.Context, limit int) error {
	if limit < 1 {
		return errors.New("model call limit must be positive")
	}
	return s.store.update(ctx, func(v *Snapshot) error {
		day := s.now().UTC().Format("2006-01-02")
		if v.ModelCalls[day] >= limit {
			return errors.New("daily model call allowance exhausted")
		}
		v.ModelCalls[day]++
		for d := range v.ModelCalls {
			if d < day {
				delete(v.ModelCalls, d)
			}
		}
		return nil
	})
}

func (s *Service) RecordActivity(ctx context.Context, projectID, kind, summary string) error {
	return s.store.update(ctx, func(v *Snapshot) error { record(v, s.now().UTC(), projectID, kind, summary); return nil })
}

func pendingOperation(v Snapshot, id string) PendingOperation {
	out := PendingOperation{ID: id, Summary: "Something stopped before it finished"}
	parts := strings.Split(id, ":")
	switch parts[0] {
	case "slack":
		out.Summary = "A Slack message may not have been handled"
	case "notify":
		out.Summary = "A notification to you may not have been sent"
	}
	for _, d := range v.Decisions {
		if contains(parts, d.ID) {
			out.ProjectID = d.ProjectID
			out.Summary += " Decision: " + d.Title
			return out
		}
	}
	for _, p := range v.Projects {
		if contains(parts, p.ID) {
			out.ProjectID = p.ID
			out.Summary += " Project: " + p.Title
			return out
		}
	}
	return out
}
