package work

import (
	"fmt"
	"sync"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
)

// Bounds on the flow shadow's reports: how many distinct disagreements a
// daemon remembers having reported, and how many it reports in one pass.
const (
	maxShadowSeen    = 1000
	maxShadowPerPass = 10
)

// flowShadow remembers which disagreements it has reported, so each is
// reported once rather than on every pass.
type flowShadow struct {
	mu   sync.Mutex
	seen map[core.FlowDisagreement]bool
}

// shadowFlows reads every task's record against its flow beside the loop
// and reports each new disagreement as a diagnostic. It runs on the
// snapshot the pass already took, so it adds no store read and holds no
// lock but its own, and it changes nothing: the flows only watch, for now.
func (lp *Loop) shadowFlows(snap *core.Snapshot) {
	found := core.FlowDisagreements(snap)
	if lp.shadowed != nil {
		lp.shadowed(found)
	}
	for _, d := range lp.flowShadow.fresh(found) {
		lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "flow_shadow", ProjectID: d.ProjectID, Code: "flow_disagreement"}, flowDisagreement(d))
	}
}

// fresh is the disagreements in found not reported before, up to the bounds.
func (s *flowShadow) fresh(found []core.FlowDisagreement) []core.FlowDisagreement {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []core.FlowDisagreement
	for _, d := range found {
		if s.seen[d] || len(s.seen) >= maxShadowSeen || len(out) >= maxShadowPerPass {
			continue
		}
		if s.seen == nil {
			s.seen = map[core.FlowDisagreement]bool{}
		}
		s.seen[d] = true
		out = append(out, d)
	}
	return out
}

// flowDisagreement carries a disagreement to diagnostics. Its fields are
// ids, statuses, stage ids and seat names, never a task's content.
type flowDisagreement core.FlowDisagreement

func (d flowDisagreement) Error() string { return d.SafeDiagnostic() }

func (d flowDisagreement) SafeDiagnostic() string {
	return fmt.Sprintf("flow shadow disagrees on task %s revision %d: %s: flow %q, loop %q", d.TaskID, d.Revision, d.Kind, d.Flow, d.Engine)
}
