package core

import (
	"context"
	"errors"
)

// SeedDemo fills an empty store with a fictional sample, for demo mode. It
// refuses a store that holds anything, so real state is never touched. The
// sample's projects give their whole playbooks, which are split between
// repositories, the sample's teams and the projects as a real project's are.
func (s *Service) SeedDemo(ctx context.Context, sample Snapshot) (Snapshot, error) {
	var out Snapshot
	err := s.store.update(ctx, func(v *Snapshot) error {
		if len(v.Projects)+len(v.Tasks)+len(v.Decisions)+len(v.Messages)+len(v.Memories)+len(v.Members) > 0 {
			return errors.New("the demo sample only fills an empty state")
		}
		for i := range sample.Projects {
			if err := s.store.prepareProject(&sample.Projects[i]); err != nil {
				return err
			}
		}
		v.Projects, v.Tasks, v.Decisions = sample.Projects, sample.Tasks, sample.Decisions
		v.Messages, v.Memories, v.Activity = sample.Messages, sample.Memories, sample.Activity
		if sample.Members != nil {
			v.Members = sample.Members
		}
		v.Teams = sample.Teams
		adoptPlaybooks(v)
		// The sample's projects get prefixes and its tasks numbers as real
		// state from before readable IDs does.
		backfillRefs(v)
		deriveStages(v)
		out = *v
		return nil
	})
	return out, err
}

// adoptPlaybooks splits each whole playbook a project was given, with no
// settings of its own, between its owners, as the project-level controls do.
func adoptPlaybooks(v *Snapshot) {
	for i := range v.Projects {
		p := &v.Projects[i]
		if p.Settings != nil || p.Playbook == nil {
			continue
		}
		pb := clonePlaybook(*p.Playbook)
		p.Playbook = nil
		placePlaybook(v, p, pb)
	}
	resolvePlaybooks(v)
}
