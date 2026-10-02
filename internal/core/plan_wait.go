package core

import (
	"regexp"
	"slices"
)

// UndeclaredWaitQuestion holds a plan whose scheduling condition is missing.
const UndeclaredWaitQuestion = "The plan says implementation waits for something but names nothing to wait for. Should it start?"

// Only statements about starting this work are scheduling instructions.
// Bare task subjects describe the product's behaviour, not this implementation.
var waitSummary = regexp.MustCompile(
	`(?i)\b(?:implementation|this\s+task|(?:the\s+)?work(?:\s+on\s+this(?:\s+task)?)?)\s+(?:waits\b|(?:cannot|can['’]t|can\s+not)\s+start\s+(?:until|before)\b|(?:is\s+)?blocked\s+(?:on|by)\b)` +
		`|\b(?:implementation|this\s+task|(?:the\s+)?work\s+on\s+this(?:\s+task)?)\s+(?:must|should|will)\s+wait\b` +
		`|\bwait\s+(?:on|for)\b[^.;]{0,80}\bbefore\s+implementing\b` +
		`|(?:^|[.;]\s+)\s*(?:it\s+)?waits\s+(?:on|for)\b[^.;]{0,80}\b(?:to\s+land|landing|tagged|released|merged|ready)\b`)

// saysImplementationWaits detects a summary that holds this implementation.
func saysImplementationWaits(summary string) bool { return waitSummary.MatchString(summary) }

// UndeclaredPlanWait checks a summary against validated dependencies and the
// prerequisite record, including conditions the owner already resolved.
// It applies only before work begins and respects the owner's answer to a
// previous undeclared-wait question when the researcher plans again.
func UndeclaredPlanWait(plan Plan, deps []string, t Task) bool {
	if len(t.Revisions) > 0 || (t.Plan != nil && t.Plan.Answered && slices.Contains(t.Plan.Questions, UndeclaredWaitQuestion)) {
		return false
	}
	if !saysImplementationWaits(plan.Summary) || len(deps) > 0 || len(plan.Prerequisites) > 0 || len(t.WaitsFor) > 0 || len(t.DependsOn) > 0 {
		return false
	}
	for _, b := range t.Blockers {
		if b.Kind == BlockerPrerequisite {
			return false
		}
	}
	return true
}
