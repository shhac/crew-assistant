package core

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// LoopSource points back to the original record, never a second history.
type LoopSource struct {
	Kind string    `json:"kind"`
	ID   string    `json:"id"`
	At   time.Time `json:"at"`
	Text string    `json:"text,omitempty"`
}
type LoopSignal struct {
	Kind      string       `json:"kind"`
	Count     int          `json:"count"`
	Threshold int          `json:"threshold"`
	Tripped   bool         `json:"tripped"`
	Summary   string       `json:"summary"`
	Sources   []LoopSource `json:"sources"`
}
type LoopReport struct {
	TaskID   string       `json:"task_id"`
	Rounds   int          `json:"rounds"`
	Limit    int          `json:"limit"`
	Attempts int          `json:"attempts"`
	Signals  []LoopSignal `json:"signals"`
	Risk     bool         `json:"risk"`
}

var loopRound = regexp.MustCompile(`(?i)\b(rounds?|attempts?)\s*#?\s*\d+\b|\bafter\s+\d+\s+rounds?\b`)
var loopRisk = regexp.MustCompile(`\bp1\b|security|data[- ]loss|irreversible`)
var loopSpace = regexp.MustCompile(`\s+`)

func loopShape(s string) string {
	return strings.TrimSpace(loopSpace.ReplaceAllString(loopRound.ReplaceAllString(strings.ToLower(s), "$1"), " "))
}
func riskyLoopText(s string) bool {
	s = strings.ToLower(s)
	return loopRisk.MatchString(s)
}
func (r LoopReport) Looping() bool {
	return slices.ContainsFunc(r.Signals, func(s LoopSignal) bool { return s.Tripped })
}

// Explain retains dated source evidence so the PM and owner can inspect each
// count. Plain round limits and routine handoff attempts are context only.
func (r LoopReport) Explain() string {
	if !r.Looping() {
		return ""
	}
	out := fmt.Sprintf("Loop signals: %d rounds used versus limit %d; %d attempts.", r.Rounds, r.Limit, r.Attempts)
	for _, s := range r.Signals {
		if !s.Tripped && s.Kind != "rounds" && s.Kind != "attempts" && s.Kind != "decision_kind" {
			continue
		}
		out += "\n" + s.Summary
		sources := s.Sources
		if !s.Tripped && len(sources) > 3 {
			sources = slices.Clone(sources)
			sort.SliceStable(sources, func(i, j int) bool { return sources[i].At.Before(sources[j].At) })
			out += fmt.Sprintf(" (latest 3 of %d sources)", len(sources))
			sources = sources[len(sources)-3:]
		}
		for _, src := range sources {
			out += fmt.Sprintf("\n  %s %s (%s): %s", src.Kind, src.ID, src.At.UTC().Format(time.RFC3339), src.Text)
		}
	}
	return out
}

// Summary is the queue look's compact view. Full history remains in the report.
// Only tripped signals and their latest source are included.
func (r LoopReport) Summary() string {
	if !r.Looping() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Loop signals: %d rounds used versus limit %d; %d attempts.", r.Rounds, r.Limit, r.Attempts)
	for _, signal := range r.Signals {
		if !signal.Tripped {
			continue
		}
		fmt.Fprintf(&b, "\n- %s (count %d; threshold %d)", text.Clip(signal.Summary, 180), signal.Count, signal.Threshold)
		if len(signal.Sources) == 0 {
			continue
		}
		latest := signal.Sources[0]
		for _, source := range signal.Sources[1:] {
			if !source.At.Before(latest.At) {
				latest = source
			}
		}
		fmt.Fprintf(&b, "\n  Latest: %s %s (%s): %s", latest.Kind, latest.ID, latest.At.UTC().Format(time.RFC3339), text.Clip(latest.Text, 160))
	}
	return b.String()
}

// loopDecisionContext removes only the explanation this code appended; new
// question content remains part of both deduplication and shape matching.
func loopDecisionContext(context string) string {
	return strings.SplitN(context, "\n\nLoop signals:", 2)[0]
}

// originalDecisionContext removes this decision's precise presentation prefix,
// so evidence does not feed back into question matching or replay checks.
func originalDecisionContext(d Decision) string {
	context := d.Context
	if d.LoopReport != nil {
		context = strings.TrimPrefix(context, d.LoopReport.OwnerExplanation()+"\n\n")
	}
	return loopDecisionContext(context)
}

// OwnerExplanation leads with one plain observation. Full counts and dated
// sources stay in the report for PM prompts and the decision's drill-down.
func (r LoopReport) OwnerExplanation() string {
	for _, s := range r.Signals {
		if !s.Tripped {
			continue
		}
		switch s.Kind {
		case "identical_decisions":
			if strings.Contains(s.Summary, "escalation") {
				return fmt.Sprintf("This is the %s identical escalation.", ordinal(s.Count))
			}
			return fmt.Sprintf("This is the %s report of the same problem.", ordinal(s.Count))
		case "owner_questions":
			return fmt.Sprintf("You have been asked the same question %d times.", s.Count)
		case "findings":
			return fmt.Sprintf("The same issue has come back %d times.", s.Count)
		case "growth":
			return fmt.Sprintf("Files and evidence have grown by %d items without resolving the same issue.", s.Count)
		case "catch_ups", "catch_up_attempts":
			return fmt.Sprintf("The team has had to catch up with other work %d times.", s.Count)
		case "research":
			return fmt.Sprintf("The team has repeated the same research %d times.", s.Count)
		case "prerequisites":
			return fmt.Sprintf("A condition that was already settled keeps coming back (%d recorded answers).", s.Count)
		}
	}
	return ""
}

func (r LoopReport) response() (policy, action string) {
	policy, action = "pause_loop", "consolidate the repeated decisions and propose a smaller core scope"
	for _, s := range r.Signals {
		if s.Tripped {
			switch s.Kind {
			case "catch_ups", "catch_up_attempts", "prerequisites":
				return "", "Ask the owner to resolve the mechanical catch-up or prerequisite loop using its history; preserve accepted-draft checked landing. No PM response was executed."
			case "growth":
				policy, action = "rescope", "keep the core purpose and move secondary requirements to a named follow-up"
			case "findings":
				policy, action = "change_approach", "review the failed-twice evidence and propose a different approach; only the owner confirms any stop"
			}
		}
	}
	return
}

// OwnerRecommendation gives one action in the owner's terms, independent of
// the technical policy catalogue provided to the PM.
func (r LoopReport) OwnerRecommendation(v Snapshot, projectID string) string {
	if r.Risk {
		return "Review the risk before choosing the next step; this stays with you."
	}
	if !r.Looping() {
		return ""
	}
	policy, _ := r.response()
	action := "Bring the repeated questions together and choose a smaller scope."
	switch policy {
	case "rescope":
		action = "Keep the core purpose and move secondary requirements to a follow-up."
	case "change_approach":
		action = "Choose a different approach; only you can confirm stopping this request."
	case "":
		action = "Wait for the other work to settle before trying again."
		for _, s := range r.Signals {
			if s.Kind == "prerequisites" && s.Tripped {
				action = "Use the recorded answers to settle what the work is waiting for."
			}
		}
		return action + " No automatic action was taken."
	}
	return action + " " + r.ownerAuthorityNote(v, projectID)
}

// ownerAuthorityNote adds no competing action to a producer's recommendation.
func (r LoopReport) ownerAuthorityNote(v Snapshot, projectID string) string {
	policy, _ := r.response()
	setting := map[string]string{"rescope": "Changing scope automatically", "change_approach": "Proposing a new approach automatically", "pause_loop": "Pausing looping work automatically"}[policy]
	if policy != "" && !PMPolicyOn(v, projectID, policy) {
		return setting + " is off; no action was taken."
	}
	return "No automatic action was taken."
}

func (r LoopReport) annotateRecommendation(v Snapshot, projectID, producer string) string {
	if strings.TrimSpace(producer) == "" {
		return r.OwnerRecommendation(v, projectID)
	}
	if r.Risk {
		return "Review the risk before choosing the next step; this stays with you. " + producer
	}
	return producer + " " + r.OwnerExplanation() + " " + r.ownerAuthorityNote(v, projectID)
}

// Recommend explains authority without executing policies a-d. They are built
// in later parts; an enabled setting alone must not imply an implemented action.
func (r LoopReport) Recommend(v Snapshot, projectID string) string {
	if r.Risk {
		return "P1, security, data loss or irreversible work: this stays with you. Review the evidence and choose the next approach."
	}
	if !r.Looping() {
		return ""
	}
	policy, action := r.response()
	if policy == "" {
		return action
	}
	state := "enabled, but execution belongs to a later part"
	if !PMPolicyOn(v, projectID, policy) {
		state = "off"
	}
	rec := fmt.Sprintf("Recommended: %s (%s is %s). Not executed; ask the owner.", action, policy, state)
	for _, option := range []struct{ key, words string }{{"rescope", "preserve the core purpose and split secondary requirements"}, {"accept_leftovers", "at the round limit, accept only safe narrow leftovers with a follow-up; if a follow-up already exists, use Accept this draft"}, {"pause_loop", "consolidate loop evidence and propose a smaller scope"}, {"change_approach", "after two failures propose a new approach; the owner confirms any stop"}} {
		setting := "On"
		if !PMPolicyOn(v, projectID, option.key) {
			setting = "Off"
		}
		rec += fmt.Sprintf("\n%s (%s): %s. Not executed in this part.", option.key, setting, option.words)
	}
	return rec
}

// LoopSignals derives all history from the snapshot. Thresholds diagnose
// recurrence, not the normal transition at max_rounds.
func LoopSignals(v Snapshot, taskID string) LoopReport {
	t := task(&v, taskID)
	if t == nil {
		return LoopReport{}
	}
	r := LoopReport{TaskID: t.ID, Rounds: t.Round, Limit: t.MaxRounds, Attempts: t.Attempt}
	if r.Limit == 0 {
		if p := project(&v, t.ProjectID); p != nil {
			if pb := v.EffectivePlaybook(*p); pb != nil {
				r.Limit = pb.MaxRounds
			}
		}
	}
	add := func(kind string, count, threshold int, trip bool, summary string, sources []LoopSource) {
		r.Signals = append(r.Signals, LoopSignal{Kind: kind, Count: count, Threshold: threshold, Tripped: trip, Summary: summary, Sources: sources})
	}
	add("rounds", t.Round, r.Limit, false, fmt.Sprintf("%d rounds used versus limit %d", t.Round, r.Limit), []LoopSource{{Kind: "task", ID: t.ID, At: t.UpdatedAt}})
	add("attempts", t.Attempt, 3, false, fmt.Sprintf("%d handoff attempts (supporting history, not failures)", t.Attempt), []LoopSource{{Kind: "task", ID: t.ID, At: t.UpdatedAt}})
	var decisions []Decision
	kinds := map[string][]LoopSource{}
	for _, d := range v.Decisions {
		if d.TaskID == t.ID {
			decisions = append(decisions, d)
			kinds[d.Kind] = append(kinds[d.Kind], LoopSource{Kind: "decision", ID: d.ID, At: d.CreatedAt, Text: d.Title})
		}
	}
	sort.SliceStable(decisions, func(i, j int) bool { return decisions[i].CreatedAt.Before(decisions[j].CreatedAt) })
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		add("decision_kind", len(kinds[k]), 3, false, fmt.Sprintf("%d %s decisions", len(kinds[k]), k), kinds[k])
	}
	if len(decisions) > 0 {
		last := decisions[len(decisions)-1]
		if last.Kind == DecisionEscalation || last.Kind == DecisionFailure {
			var sources []LoopSource
			for i := len(decisions) - 1; i >= 0; i-- {
				d := decisions[i]
				if d.Kind != last.Kind || loopShape(d.Title) != loopShape(last.Title) {
					break
				}
				sources = append(sources, LoopSource{Kind: "decision", ID: d.ID, At: d.CreatedAt, Text: d.Title})
			}
			add("identical_decisions", len(sources), 2, len(sources) >= 2, fmt.Sprintf("This is the %s identical %s.", ordinal(len(sources)), last.Kind), sources)
		}
	}
	questions := map[string][]LoopSource{}
	for _, d := range decisions {
		if d.Kind == DecisionQuestion {
			key := loopShape(d.Title + " " + originalDecisionContext(d))
			questions[key] = append(questions[key], LoopSource{Kind: "decision", ID: d.ID, At: d.CreatedAt, Text: d.Title + " " + originalDecisionContext(d)})
		}
	}
	keys = keys[:0]
	for k := range questions {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ss := questions[k]
		add("owner_questions", len(ss), 2, len(ss) >= 2, fmt.Sprintf("%d owner questions of the same shape: %s", len(ss), k), ss)
	}
	// Count the same finding once per distinct draft. Later passing reviews of
	// the latest draft retire recurrence rather than keeping a permanent alarm.
	latest := 0
	if len(t.Revisions) > 0 {
		latest = t.Revisions[len(t.Revisions)-1].N
	}
	checks := []Verdict{}
	seenChecks := map[string]bool{}
	for i := len(t.Verdicts) - 1; i >= 0; i-- {
		check := t.Verdicts[i]
		if check.TextVersion != t.TextVersion || check.Answered {
			continue
		}
		if p := project(&v, t.ProjectID); p != nil && check.BriefVersion != 0 && check.BriefVersion != p.Brief.Version {
			continue
		}
		key := fmt.Sprintf("%d:%s", check.Revision, check.Role)
		if seenChecks[key] {
			continue
		}
		seenChecks[key] = true
		checks = append(checks, check)
	}
	slices.Reverse(checks)
	currentPass := false
	for _, check := range checks {
		if check.Revision == latest && check.TextVersion == t.TextVersion && check.Outcome == VerdictPass {
			currentPass = true
		}
	}
	// A passing checker does not erase another checker's current findings.
	for _, check := range checks {
		if check.Revision == latest && check.Outcome != VerdictPass && len(check.Findings) > 0 {
			currentPass = false
		}
	}
	findings := map[string][]LoopSource{}
	revisions := map[string]map[int]bool{}
	for _, check := range checks {
		if check.TextVersion != t.TextVersion {
			continue
		}
		for _, f := range check.Findings {
			key := loopShape(f.Criterion + " " + f.Note)
			r.Risk = r.Risk || (check.Revision == latest && riskyLoopText(f.Note+" "+f.Criterion))
			if revisions[key] == nil {
				revisions[key] = map[int]bool{}
			}
			if revisions[key][check.Revision] {
				continue
			}
			revisions[key][check.Revision] = true
			id := check.ID
			if id == "" {
				id = fmt.Sprintf("%s/draft/%d/%s/%s", t.ID, check.Revision, check.Role, check.At.UTC().Format(time.RFC3339Nano))
			}
			findings[key] = append(findings[key], LoopSource{Kind: "verdict", ID: id, At: check.At, Text: fmt.Sprintf("draft %d: %s", check.Revision, f.Note)})
		}
	}
	keys = keys[:0]
	for k := range findings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	unresolved := false
	for _, k := range keys {
		ss := findings[k]
		active := revisions[k][latest]
		trip := len(ss) >= 2 && active && !currentPass
		unresolved = unresolved || trip
		add("findings", len(ss), 2, trip, fmt.Sprintf("The same finding appears across %d drafts: %s", len(ss), k), ss)
	}
	var merges []LoopSource
	for i := len(t.Revisions) - 1; i >= 0; i-- {
		rev := t.Revisions[i]
		if rev.CleanMergeOf == 0 {
			break
		}
		merges = append(merges, LoopSource{Kind: "revision", ID: fmt.Sprintf("%s/draft/%d", t.ID, rev.N), At: rev.At, Text: fmt.Sprintf("clean merge of draft %d (%s)", rev.CleanMergeOf, rev.Ref)})
	}
	accepted := false
	if p := project(&v, t.ProjectID); p != nil {
		// An owner hold does not turn ordinary accepted-draft catch-ups into
		// failed progress. ResumeStatus describes real landing failure holds;
		// an empty continuation still holds the same accepted work for its owner.
		status := t.Status
		if status == TaskWaiting && t.ResumeStatus != "" {
			status = t.ResumeStatus
		}
		landing := status == TaskLanding || status == TaskAwaiting || (t.Status == TaskWaiting && t.ResumeStatus == "")
		// A failed validation is still a check of the same accepted merge,
		// not evidence that its preceding clean catch-ups were failed drafts.
		validation := t.MergeValidation
		checkedLanding := validation != nil && validation.Revision == latest && validation.BriefVersion == p.Brief.Version && validation.TextVersion == t.TextVersion
		accepted = t.AcceptanceStands(p.Brief.Version) && (landing || t.Delivering != nil || checkedLanding)
	}
	add("catch_ups", len(merges), 2, len(merges) >= 2 && !accepted, fmt.Sprintf("%d clean catch-ups since the last substantive draft (accepted checked landing: %t)", len(merges), accepted), merges)
	if t.CatchUps > 4 {
		add("catch_up_attempts", t.CatchUps, 5, true, fmt.Sprintf("%d landing catch-up attempts; the target keeps moving", t.CatchUps), append([]LoopSource{{Kind: "task", ID: t.ID, At: t.UpdatedAt, Text: "CatchUps counter; target keeps moving"}}, merges...))
	}
	var substantive []Revision
	for _, rev := range t.Revisions {
		if rev.CleanMergeOf == 0 {
			substantive = append(substantive, rev)
		}
	}
	if len(substantive) >= 2 {
		first, last := substantive[0], substantive[len(substantive)-1]
		growth := len(last.Files) - len(first.Files)
		sources := []LoopSource{{Kind: "revision", ID: fmt.Sprintf("%s/draft/%d", t.ID, first.N), At: first.At, Text: fmt.Sprintf("%d changed files", len(first.Files))}, {Kind: "revision", ID: fmt.Sprintf("%s/draft/%d", t.ID, last.N), At: last.At, Text: fmt.Sprintf("%d changed files", len(last.Files))}}
		evidence := 0
		for _, a := range t.Attachments {
			if a.Verdict != "" || a.Design != "" {
				if !a.At.Before(first.At) {
					evidence++
					sources = append(sources, LoopSource{Kind: "attachment", ID: a.ID, At: a.At, Text: a.Name})
				}
			}
		}
		add("growth", max(growth, 0)+evidence, 1, unresolved && !accepted && (growth > 0 || evidence > 0), fmt.Sprintf("Changed files grew by %d; %d evidence attachments while the same core finding remains unresolved", growth, evidence), sources)
	}
	research := map[string][]LoopSource{}
	for _, request := range t.Research {
		k := loopShape(request.Question)
		research[k] = append(research[k], LoopSource{Kind: "research", ID: request.ID, At: request.At, Text: request.Question})
	}
	keys = keys[:0]
	for k := range research {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ss := research[k]
		add("research", len(ss), 3, len(ss) >= 3, fmt.Sprintf("%d repeated research requests: %s", len(ss), k), ss)
	}
	var prerequisites []LoopSource
	for _, b := range t.Blockers {
		if b.Kind == BlockerPrerequisite {
			for _, settled := range b.Settlements {
				prerequisites = append(prerequisites, LoopSource{Kind: "blocker", ID: b.ID, At: settled.At, Text: b.Description + ": " + settled.Answer})
			}
		}
	}
	add("prerequisites", len(prerequisites), 2, slices.ContainsFunc(t.Blockers, func(b Blocker) bool { return b.Kind == BlockerPrerequisite && len(b.Settlements) >= 2 }), fmt.Sprintf("%d prerequisite settlements; check for repeated prerequisite requests", len(prerequisites)), prerequisites)
	return r
}
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
