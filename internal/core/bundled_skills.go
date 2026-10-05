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
		if p.Settings == nil {
			return fmt.Errorf("choose a team first")
		}
		if slices.Contains(p.Settings.DisabledBundledSkills, name) == !enabled {
			out = *p
			return nil
		}
		// Skills are the project's own setting, whoever staffs it.
		before := snapshotCopy(v)
		own := clonePlaybook(*p.Settings)
		own.DisabledBundledSkills = slices.DeleteFunc(own.DisabledBundledSkills, func(n string) bool { return n == name })
		if !enabled {
			own.DisabledBundledSkills = append(own.DisabledBundledSkills, name)
		}
		slices.Sort(own.DisabledBundledSkills)
		p.Settings = &own
		if err := settle(before, v, p.ID, "", "", Playbook.Validate); err != nil {
			return err
		}
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "playbook.skill", fmt.Sprintf("Bundled skill %s enabled: %t", name, enabled))
		out = *p
		return nil
	})
	return out, err
}
