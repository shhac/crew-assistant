package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	return s.editProject(ctx, projectID, func(p *Project, v *Snapshot) error {
		now := s.now().UTC()
		before := p.Brief.Criteria
		p.Brief = Brief{Version: p.Brief.Version + 1, Goal: strings.TrimSpace(in.Goal), Audience: strings.TrimSpace(in.Audience), Constraints: strings.TrimSpace(in.Constraints), Criteria: cleanList(in.Criteria), UpdatedAt: now}
		// The outgoing brief is the evidence for older reports that have no
		// binding. Retire only requirements absent from both current sources,
		// in the same transaction that replaces that evidence.
		for i := range v.Tasks {
			t := &v.Tasks[i]
			if t.ProjectID != p.ID || t.Finished() {
				continue
			}
			retire := func(reports []Unreachable) []Unreachable {
				return slices.DeleteFunc(reports, func(u Unreachable) bool {
					removed := slices.Contains(before, u.Criterion) && !slices.Contains(p.Brief.Criteria, u.Criterion) && !slices.Contains(t.Criteria, u.Criterion)
					if removed {
						recordTask(v, now, t, "task.asset_obsolete", u.Criterion+": report retired after brief requirement removal")
					}
					return removed
				})
			}
			t.Unreachable = retire(t.Unreachable)
			if t.Handoff != nil {
				t.Handoff.Unreachable = retire(t.Handoff.Unreachable)
			}
			reconcileAssetIntegration(t, p)
			for j := range t.Design {
				if t.Design[j].Open() {
					t.Design[j].AssetReports = retire(t.Design[j].AssetReports)
				}
			}
			derive(v, t)
		}
		p.UpdatedAt = now
		record(v, now, p.ID, "brief.updated", fmt.Sprintf("Brief is now version %d", p.Brief.Version))
		return nil
	})
}
