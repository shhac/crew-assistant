package core

import (
	"context"
	"fmt"
	"slices"

	"github.com/shhac/crew-assistant/internal/bundledskills"
)

// SetBundledSkill changes one setting transactionally, retaining unrelated
// team edits. Existing tasks retain their pinned settings until adoption.
func (s *Service) SetBundledSkill(ctx context.Context, projectID, name string, enabled bool) (Project, error) {
	if !bundledskills.Known(name) {
		return Project{}, fmt.Errorf("unknown bundled skill %q", name)
	}
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, projectID)
		if p == nil {
			return ErrNotFound
		}
		if p.Playbook == nil {
			return fmt.Errorf("choose a team first")
		}
		if slices.Contains(p.Playbook.DisabledBundledSkills, name) == !enabled {
			out = *p
			return nil
		}
		pb := *p.Playbook
		pb.DisabledBundledSkills = slices.DeleteFunc(slices.Clone(pb.DisabledBundledSkills), func(n string) bool { return n == name })
		if !enabled {
			pb.DisabledBundledSkills = append(pb.DisabledBundledSkills, name)
		}
		slices.Sort(pb.DisabledBundledSkills)
		if err := pb.Validate(); err != nil {
			return err
		}
		p.Playbook = &pb
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "playbook.skill", fmt.Sprintf("Bundled skill %s enabled: %t", name, enabled))
		out = *p
		return nil
	})
	return out, err
}
