package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Brief is what a project is for. Every revision and verdict records the
// version it was made against, so work judged against an older brief is never
// mistaken for work judged against the current one.
type Brief struct {
	Version     int       `json:"version"`
	Goal        string    `json:"goal"`
	Audience    string    `json:"audience,omitempty"`
	Constraints string    `json:"constraints,omitempty"`
	Criteria    []string  `json:"criteria"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type BriefInput struct {
	Goal        string   `json:"goal"`
	Audience    string   `json:"audience"`
	Constraints string   `json:"constraints"`
	Criteria    []string `json:"criteria"`
}

func cleanList(items []string) []string {
	out := []string{}
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// UpdateBrief replaces a project's brief with a new version.
func (s *Service) UpdateBrief(ctx context.Context, projectID string, in BriefInput) (Project, error) {
	if !required(in.Goal) {
		return Project{}, errors.New("a brief needs a goal")
	}
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		p.Brief = Brief{Version: p.Brief.Version + 1, Goal: strings.TrimSpace(in.Goal), Audience: strings.TrimSpace(in.Audience), Constraints: strings.TrimSpace(in.Constraints), Criteria: cleanList(in.Criteria), UpdatedAt: now}
		p.UpdatedAt = now
		out = *p
		record(v, now, p.ID, "brief.updated", fmt.Sprintf("Brief is now version %d", p.Brief.Version))
		return nil
	})
	return out, err
}
