package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// escalationChoices are what the owner can do with a task that still has
// review points at its round limit.
var escalationChoices = []string{choiceAnotherRound, choiceAcceptFollowUp, choiceAcceptDraft, choiceStop}

// escalate brings the owner a task that still has review points at its
// round limit. Where the project has a PM, the PM judges what remains first,
// and the owner gets one recommendation, chosen from that judgement by a
// fixed rule; see recommend. Either way the owner can accept the draft and
// queue what remains as a follow-up, which the decision holds exactly as
// the owner reads it.
func (lp *Loop) escalate(ctx context.Context, p core.Project, t core.Task, current, changes []core.Verdict) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	findings := remaining(changes)
	passed := checksPassed(t, current)
	waiting := waitingOn(snap, t)
	in := core.DecisionInput{
		Title:          fmt.Sprintf("“%s” still has review points after %d rounds", t.Objective, t.Round),
		Recommendation: "Another round if these points matter; otherwise accept it as it is, or accept it and queue them as a follow-up",
		Choices:        escalationChoices,
		FollowUp:       followUpFrom(t, findings),
	}
	var b strings.Builder
	b.WriteString(reviewDigest(changes))
	seat, judged, busy := lp.judgeEscalation(ctx, p, t, escalationPrompt(snap, p, t, current, findings, passed, waiting), len(findings))
	if busy != "" {
		return lp.waitForSeat(ctx, t, busy)
	}
	if _, ok := t.Designer(); ok && judged == nil {
		for _, f := range findings {
			classified := slices.ContainsFunc(t.Unreachable, func(u core.Unreachable) bool {
				if u.ID != "" {
					return u.ID == reviewReport(p, t, f, nil).ID && u.AssetCreation != nil && !*u.AssetCreation
				}
				return u.Source == "review" && u.Revision == len(t.Revisions) && u.TextVersion == t.TextVersion && u.Finding == f.Note && u.Criterion == reviewRequirement(p, t, f) && u.AssetCreation != nil && !*u.AssetCreation
			})
			if !classified {
				return lp.routeAsset(ctx, p, t, reviewReport(p, t, f, nil), "classification required")
			}
		}
	}
	if judged != nil {
		if _, ok := t.Designer(); ok {
			for _, j := range judged.Findings {
				if j.AssetCreation != nil && *j.AssetCreation {
					f := findings[j.Finding-1]
					return lp.routeAsset(ctx, p, t, reviewReport(p, t, f, j.AssetCreation), seat)
				}
			}
		}
		choice, why := recommend(*judged, findings, passed, waiting)
		in.Recommendation = fmt.Sprintf("%s: %s. %s", choice, why, judged.Reason)
		if f := judged.FollowUp; f != nil {
			criteria := slices.DeleteFunc(slices.Clone(f.Criteria), func(c string) bool { return strings.TrimSpace(c) == "" })
			if objective := strings.TrimSpace(f.Objective); objective != "" && len(criteria) > 0 {
				in.FollowUp = &core.TaskInput{Objective: objective, Criteria: criteria}
			}
		}
		fmt.Fprintf(&b, "\n\n%s judged what remains:\n", seat)
		for _, j := range judged.Findings {
			fmt.Fprintf(&b, "%d. %s: %s\n", j.Finding, findings[j.Finding-1].Note, j.words())
		}
		if len(waiting) > 0 && judged.WaitingNeedsFix {
			b.WriteString("A task waiting on it needs what remains fixed first.\n")
		} else if len(waiting) > 0 {
			b.WriteString("The tasks waiting on it don't need what remains fixed first.\n")
		}
	}
	if !passed {
		b.WriteString("\nA check failed on this draft.")
	}
	if len(waiting) > 0 {
		b.WriteString("\nWaiting on it:")
		for _, w := range waiting {
			fmt.Fprintf(&b, "\n- %s", w.Objective)
		}
	}
	fmt.Fprintf(&b, "\n\nIf you accept and follow up, this is queued right after it, waiting for it to land:\n%s", in.FollowUp.Objective)
	for _, c := range in.FollowUp.Criteria {
		fmt.Fprintf(&b, "\n- %s", c)
	}
	in.Context = strings.TrimSpace(b.String())
	_, err = lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionEscalation, in)
	return err
}

// finding is one point a checker still raises, as the owner and the PM read
// it.
type finding struct {
	Role, Criterion, Note string
}

// remaining is every point the checks that send the task back still raise:
// each finding, or a verdict's note or summary where it lists none.
func remaining(changes []core.Verdict) []finding {
	var out []finding
	for _, v := range changes {
		for _, f := range v.Findings {
			out = append(out, finding{Role: v.Role, Criterion: f.Criterion, Note: f.Note})
		}
		if len(v.Findings) == 0 {
			note := v.Note
			if note == "" {
				note = v.Summary
			}
			out = append(out, finding{Role: v.Role, Note: note})
		}
	}
	return out
}

// checksPassed reports whether every check QA ran on the draft passed. A
// team without QA runs none, so none failed.
func checksPassed(t core.Task, current []core.Verdict) bool {
	for _, v := range current {
		if r, ok := t.Role(v.Role); ok && r.Holds(core.RoleQA) && v.Outcome != core.VerdictPass {
			return false
		}
	}
	return true
}

// waitingOn is the project's unfinished tasks that depend on t.
func waitingOn(snap core.Snapshot, t core.Task) []core.Task {
	var out []core.Task
	for _, other := range snap.Tasks {
		if other.ProjectID == t.ProjectID && !other.Finished() && slices.Contains(other.DependsOn, t.ID) {
			out = append(out, other)
		}
	}
	return out
}

// followUpFrom is the follow-up made from the findings as they are, when
// no PM wrote one: each finding a criterion naming what it applies to.
func followUpFrom(t core.Task, findings []finding) *core.TaskInput {
	ref := t.Ref
	if ref == "" {
		ref = t.ID
	}
	in := &core.TaskInput{Objective: fmt.Sprintf("Follow-up to %s: %s", ref, t.Objective)}
	for _, f := range findings {
		c := f.Note
		if f.Criterion != "" {
			c = f.Criterion + ": " + f.Note
		}
		in.Criteria = append(in.Criteria, c)
	}
	return in
}

// escalationJudgement is the PM's reading of what remains at the round
// limit.
type escalationJudgement struct {
	Findings []findingJudgement `json:"findings"`
	WrongWay *bool              `json:"wrong_way"`
	// WaitingNeedsFix is whether a task waiting on this one needs what
	// remains fixed before it can build on it.
	WaitingNeedsFix bool            `json:"waiting_needs_fix"`
	Reason          string          `json:"reason"`
	FollowUp        *core.TaskInput `json:"follow_up"`
}

// findingJudgement is the PM's reading of one remaining finding, by its
// number in the prompt.
type findingJudgement struct {
	AssetCreation *bool `json:"asset_creation"`
	Finding       int   `json:"finding"`
	Narrow        bool  `json:"narrow"`
	Regression    bool  `json:"regression"`
	Repeat        bool  `json:"repeat"`
	Core          bool  `json:"core"`
}

func (j findingJudgement) words() string {
	words := []string{"broad"}
	if j.Narrow {
		words[0] = "narrow"
	}
	if j.Regression {
		words = append(words, "a regression")
	}
	if j.Repeat {
		words = append(words, "raised in an earlier round too")
	} else {
		words = append(words, "new")
	}
	if j.Core {
		words = append(words, "about the task's core purpose")
	}
	return strings.Join(words, ", ")
}

// recommend is the one choice the owner is recommended at the round limit,
// and why, from the PM's judgement, whether the checks passed and the tasks
// waiting on this one. A draft heading the wrong way stops; a failing check,
// or a finding that is a regression, a repeat, about the task's core purpose
// or not narrow, is worth another round, as is a waiting task that needs
// what remains fixed first, since it waits only for this task and could
// start before the follow-up lands. Narrow, new findings on a draft whose
// checks pass are accepted and followed up, which lets the waiting tasks go
// on.
func recommend(j escalationJudgement, findings []finding, checksPassed bool, waiting []core.Task) (string, string) {
	if j.WrongWay != nil && *j.WrongWay {
		return choiceStop, "the draft is heading the wrong way"
	}
	if !checksPassed {
		return choiceAnotherRound, "a check is failing"
	}
	for _, f := range j.Findings {
		note := text.Clip(findings[f.Finding-1].Note, 120)
		switch {
		case f.Regression:
			return choiceAnotherRound, fmt.Sprintf("“%s” is a regression", note)
		case f.Repeat:
			return choiceAnotherRound, fmt.Sprintf("“%s” was raised in an earlier round too", note)
		case f.Core:
			return choiceAnotherRound, fmt.Sprintf("“%s” leaves the task's core purpose unmet", note)
		case !f.Narrow:
			return choiceAnotherRound, fmt.Sprintf("“%s” is not narrow enough to follow up", note)
		}
	}
	switch {
	case len(waiting) > 0 && j.WaitingNeedsFix:
		return choiceAnotherRound, fmt.Sprintf("“%s” waits on this task and needs what remains fixed first", text.Clip(waiting[0].Objective, 120))
	case len(waiting) == 1:
		return choiceAcceptFollowUp, "the checks pass and what remains is narrow and new; accepting it lets the task waiting on it go on"
	case len(waiting) > 1:
		return choiceAcceptFollowUp, fmt.Sprintf("the checks pass and what remains is narrow and new; accepting it lets the %d tasks waiting on it go on", len(waiting))
	}
	return choiceAcceptFollowUp, "the checks pass and what remains is narrow and new"
}

// judgeEscalation asks the project's PM, if it has one, to judge what
// remains of a task at its round limit. It gives the PM's name and its
// judgement, or nil when there is no PM or it can't say; a PM at work on
// something else is reported busy, to wait for. The PM may look tasks up
// and leave notes, but changes nothing it judges, and queues nothing: the
// follow-up is the owner's to choose.
func (lp *Loop) judgeEscalation(ctx context.Context, p core.Project, t core.Task, prompt string, findings int) (string, *escalationJudgement, string) {
	seat, ok := p.PMSeat()
	if !ok || findings == 0 {
		return "", nil, ""
	}
	if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
		return "", nil, ""
	}
	if held, err := lp.holdSeat(ctx, t.ID, seat.Name); err != nil || !held {
		return "", nil, seat.Name
	}
	dir, err := lp.pmWorkDir()
	if err != nil {
		return "", nil, ""
	}
	spec := lp.baseSpec(seat, dir, prompt)
	spec.ProjectID, spec.Role = p.ID, core.RolePM
	spec.TaskID = t.ID
	spec.Observer = lp.watchTurn(t, core.RolePM, seat, dir, false)
	lp.withTools(&spec, lp.answerTools(p.ID, seat))
	var judged escalationJudgement
	// Other invalid judgement fields must not erase an explicit asset finding
	// if the correction fails. Only a complete judgement replaces it.
	var assetFindings []findingJudgement
	_, _, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		var reported struct {
			Findings []json.RawMessage `json:"findings"`
		}
		if decodeReply(reply, &reported) == nil {
			for _, raw := range reported.Findings {
				var f struct {
					Finding       int   `json:"finding"`
					AssetCreation *bool `json:"asset_creation"`
				}
				if json.Unmarshal(raw, &f) != nil {
					continue
				}
				if f.Finding > 0 && f.Finding <= findings && f.AssetCreation != nil && *f.AssetCreation {
					assetFindings = append(assetFindings, findingJudgement{Finding: f.Finding, AssetCreation: f.AssetCreation})
				}
			}
		}
		judged, err = parseEscalation(reply, findings)
		if err == nil {
			if _, ok := t.Designer(); ok {
				for _, j := range judged.Findings {
					if j.AssetCreation == nil {
						return errors.New("classify every finding with asset_creation true or false")
					}
				}
			}
		}
		return err
	})
	if err != nil || parseErr != nil {
		if hasProductionDesigner(t) && len(assetFindings) > 0 {
			return seat.Name, &escalationJudgement{Findings: assetFindings}, ""
		}
		return "", nil, ""
	}
	return seat.Name, &judged, ""
}

// parseEscalation reads the PM's judgement of the n findings it was shown,
// each of which it must judge once.
func parseEscalation(reply string, n int) (escalationJudgement, error) {
	var in escalationJudgement
	if err := decodeReply(reply, &in); err != nil {
		return escalationJudgement{}, errors.New("the reply was not valid JSON")
	}
	in.Reason = text.Clip(strings.TrimSpace(in.Reason), 300)
	if in.WrongWay == nil || in.Reason == "" {
		return escalationJudgement{}, errors.New(`the reply needs "wrong_way" and a "reason"`)
	}
	seen := map[int]bool{}
	for _, f := range in.Findings {
		if f.Finding < 1 || f.Finding > n || seen[f.Finding] {
			return escalationJudgement{}, fmt.Errorf("judge each finding from 1 to %d once", n)
		}
		seen[f.Finding] = true
	}
	if len(seen) != n {
		return escalationJudgement{}, fmt.Errorf("judge every finding from 1 to %d", n)
	}
	slices.SortFunc(in.Findings, func(a, b findingJudgement) int { return a.Finding - b.Finding })
	return in, nil
}

// escalationPrompt asks the PM to judge what remains of a task at its round
// limit: the task, every check of its latest draft, what earlier rounds
// raised, whether the checks passed and what waits on it.
func escalationPrompt(snap core.Snapshot, p core.Project, t core.Task, current []core.Verdict, findings []finding, passed bool, waiting []core.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the work moving for the project %s. Goal: %s\n\nTask %s: %s\n", p.Title, p.Brief.Goal, t.Label(), text.Clip(t.Objective, 500))
	if len(t.Criteria) > 0 {
		b.WriteString("Its criteria:\n" + numbered(t.Criteria))
	}
	r := t.Revisions[len(t.Revisions)-1]
	fmt.Fprintf(&b, "\nIt has had %d rounds, its limit, and the checks of draft %d still send it back:\n", t.Round, r.N)
	for _, v := range current {
		fmt.Fprintf(&b, "- %s: %s. %s\n", v.Role, v.Outcome, text.Clip(v.Summary, 300))
		for _, f := range v.Findings {
			fmt.Fprintf(&b, "  - %s\n", text.Clip(f.Note, 300))
		}
	}
	if passed {
		b.WriteString("Every check QA ran passed.\n")
	} else {
		b.WriteString("A check QA ran failed.\n")
	}
	b.WriteString("\nWhat earlier rounds raised:\n")
	earlier := false
	for _, v := range t.Verdicts {
		if v.Revision >= r.N || (v.Outcome != core.VerdictRevise && v.Next != core.NextRevise) {
			continue
		}
		earlier = true
		fmt.Fprintf(&b, "- draft %d, %s: %s\n", v.Revision, v.Role, text.Clip(v.Summary, 300))
		for _, f := range v.Findings {
			fmt.Fprintf(&b, "  - %s\n", text.Clip(f.Note, 300))
		}
	}
	if !earlier {
		b.WriteString("- nothing\n")
	}
	b.WriteString("\nTasks waiting on this one:\n")
	for _, w := range waiting {
		fmt.Fprintf(&b, "- %s (%s): %s\n", w.Label(), w.Status, text.Clip(w.Objective, 300))
	}
	if len(waiting) == 0 {
		b.WriteString("- none\n")
	}
	b.WriteString("\nThe findings that remain, numbered:\n")
	for i, f := range findings {
		about := ""
		if f.Criterion != "" {
			about = " [" + f.Criterion + "]"
		}
		fmt.Fprintf(&b, "%d. %s%s: %s\n", i+1, f.Role, about, text.Clip(f.Note, 400))
	}
	b.WriteString(pmToldText(snap, p))
	b.WriteString(`
Judge what remains at the round limit, for the owner, who decides whether it gets another round, is accepted as it is, is accepted with a follow-up task for what remains, or stops. For each finding say whether it is narrow (a small, contained fix), whether it is a regression (it breaks something that worked), whether it repeats something an earlier round raised, and whether it is about the task's core purpose. Say whether the draft is heading the wrong way altogether, and whether any task waiting on this one needs what remains fixed before it can build on it: it waits only for this task, so it may start before a follow-up lands. Write the follow-up task for what remains so it stands alone, for someone who has not read this task. Only judge: do not change, queue or approve anything.

Reply with only this JSON object:
{"findings": [{"finding": 1, "narrow": true, "regression": false, "repeat": false, "core": false}], "wrong_way": false, "waiting_needs_fix": false, "reason": "one short line the owner reads", "follow_up": {"objective": "...", "criteria": ["..."]}}`)
	reply := b.String()
	if hasProductionDesigner(t) {
		reply = strings.ReplaceAll(reply, `"owner_step": true or false`, `"asset_creation": true or false, "owner_step": true or false`)
		reply = strings.ReplaceAll(reply, `"finding": 1, "narrow"`, `"finding": 1, "asset_creation": false, "narrow"`)
	}
	return reply + assetRule(t)
}

// Requirements the team can't meet from its sandbox.

// ownerStepEntry is one requirement in the implementer's owner-step block.
type ownerStepEntry struct {
	ReportID      string `json:"report_id,omitempty"`
	AssetCreation *bool  `json:"asset_creation"`
	Requirement   string `json:"requirement"`
	Why           string `json:"why"`
}

// parseOwnerSteps reads the implementer's owner-step block: the
// requirements it can't meet from its sandbox on draft n, each matched to
// the task or brief's current criterion where it quotes one. A block that can't be read
// says nothing. The second result is what the owner already kept for the
// team: it belongs in this draft's hand-over rather than another decision.
func parseOwnerSteps(block string, n int, criteria, owners, kept []string) ([]core.Unreachable, []core.Unreachable) {
	if block == "" {
		return nil, nil
	}
	entries, err := ownerStepEntries(block)
	if err != nil {
		return nil, nil
	}
	var out, retained []core.Unreachable
	for _, e := range entries {
		c := matchCriterion(criteria, e.Requirement)
		if slices.Contains(kept, c) {
			if !slices.ContainsFunc(retained, func(u core.Unreachable) bool { return u.Criterion == c }) {
				retained = append(retained, core.Unreachable{ID: e.ReportID, AssetCreation: e.AssetCreation, Criterion: c, Why: text.Clip(strings.TrimSpace(e.Why), 600), Revision: n})
			}
			continue
		}
		// A step already the owner's would come back reworded, round
		// after round.
		// A current team criterion can be an edited part of a split brief
		// requirement. Its new text must not disappear into the old quote.
		if slices.Contains(owners, matchCriterion(owners, e.Requirement)) && (slices.Contains(owners, c) || !slices.Contains(criteria, c)) {
			continue
		}
		if c == "" || slices.ContainsFunc(out, func(u core.Unreachable) bool { return u.Criterion == c && u.ID == e.ReportID }) {
			continue
		}
		out = append(out, core.Unreachable{ID: e.ReportID, AssetCreation: e.AssetCreation, Criterion: c, Why: text.Clip(strings.TrimSpace(e.Why), 600), Revision: n})
	}
	return out, retained
}

// matchCriterion is the task's criterion quoted: the one it equals, or the
// only one it is part of or holds; otherwise the quote as it is.
func matchCriterion(criteria []string, quoted string) string {
	quoted = strings.TrimSpace(quoted)
	if quoted == "" {
		return ""
	}
	fold := strings.ToLower(quoted)
	var found []string
	for _, c := range criteria {
		lc := strings.ToLower(strings.TrimSpace(c))
		if lc == fold {
			return c
		}
		if strings.Contains(lc, fold) || strings.Contains(fold, lc) {
			found = append(found, c)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return text.Clip(quoted, 500)
}

// ownerStepJudgement is the PM's reading of a requirement the implementer
// says it can't meet.
type ownerStepJudgement struct {
	AssetCreation *bool  `json:"asset_creation"`
	OwnerStep     *bool  `json:"owner_step"`
	Step          string `json:"step"`
	Reason        string `json:"reason"`
}

// proposeOwnerStep brings the owner a requirement the implementer says it
// can't meet from its sandbox, proposing it becomes a step the owner checks
// after the change lands, rather than another round the implementer can't
// win. The PM, if the project has one, first judges whether it truly can't
// be met there and words the step; one it says the team can meet stays with
// the team, and the task goes on as its checks say.
func (lp *Loop) proposeOwnerStep(ctx context.Context, p core.Project, t core.Task, u core.Unreachable) error {
	_, designer := t.Designer()
	if designer && u.AssetCreation != nil && *u.AssetCreation {
		return lp.routeAsset(ctx, p, t, u, "implementer")
	}
	step := core.OwnerStep{Criterion: u.Criterion, Step: u.Criterion}
	rec := "Make it an owner step if the team truly can't meet it from its sandbox; otherwise keep it for the team"
	var b strings.Builder
	fmt.Fprintf(&b, "The implementer can't meet this requirement from its sandbox:\n%s\n\nWhy: %s", u.Criterion, orDash(u.Why))
	if seat, ok := p.PMSeat(); ok {
		judged, busy := lp.judgeOwnerStep(ctx, p, t, seat, u)
		if busy != "" {
			return lp.waitForSeat(ctx, t, busy)
		}
		if designer && judged != nil && judged.AssetCreation != nil && *judged.AssetCreation {
			u.AssetCreation = judged.AssetCreation
			return lp.routeAsset(ctx, p, t, u, seat.Name)
		}
		if designer && u.AssetCreation == nil && (judged == nil || judged.AssetCreation == nil) {
			return lp.routeAsset(ctx, p, t, u, "classification required")
		}
		if judged != nil && judged.AssetCreation != nil {
			u.AssetCreation = judged.AssetCreation
		}
		if judged != nil && !*judged.OwnerStep {
			// The implementer reads why in the task's notes.
			said := fmt.Sprintf("The team can meet “%s” from its sandbox: %s", text.Clip(u.Criterion, 200), judged.Reason)
			if _, err := lp.Core.AddNote(ctx, core.NoteInput{Project: t.ProjectID, Task: t.ID, By: seat.Name, Kind: core.RolePM, While: core.TaskDeciding, Text: said}); err != nil && !errors.Is(err, core.ErrConflict) {
				return err
			}
			return lp.keepForTeam(ctx, t, u.Criterion, seat.Name+": "+said, false)
		}
		if judged != nil {
			if s := strings.TrimSpace(judged.Step); s != "" {
				step.Step = text.Clip(s, 500)
			}
			rec = fmt.Sprintf("%s: %s", choiceOwnerStep, judged.Reason)
			fmt.Fprintf(&b, "\n\n%s agrees the team can't meet it: %s", seat.Name, judged.Reason)
		}
	}
	if designer && u.AssetCreation == nil {
		return lp.routeAsset(ctx, p, t, u, "classification required")
	}
	fmt.Fprintf(&b, "\n\nAs an owner step, it leaves the team's requirements and the delivery carries:\nAfter it lands, check:\n- [ ] %s", step.Step)
	b.WriteString("\n\nYou can split it: keep part with the team and check the rest after it lands.")
	_, err := lp.Core.OpenTaskDecision(ctx, t.ID, core.DecisionEscalation, core.DecisionInput{
		Title:          fmt.Sprintf("“%s” has a requirement the team can't meet from its sandbox", t.Objective),
		Context:        b.String(),
		Recommendation: rec,
		Choices:        []string{choiceOwnerStep, choiceSplit, choiceKeepForTeam, choiceStop},
		OwnerStep:      &step,
	})
	return err
}

// keepForTeam leaves a requirement the implementer said it can't meet with
// the team, and has the task decided again from its checks.
func (lp *Loop) keepForTeam(ctx context.Context, t core.Task, criterion, activity string, owner bool) error {
	_, err := lp.updateOpen(ctx, t.ID, func(t *core.Task, _ *core.Project) (string, error) {
		if owner && !slices.Contains(t.TeamKept, criterion) {
			t.TeamKept = append(t.TeamKept, criterion)
		}
		t.SettleUnreachable(criterion)
		t.Status, t.DecisionID, t.Detail = core.TaskDeciding, "", "Kept for the team"
		return activity, nil
	})
	return err
}

// judgeOwnerStep asks the PM whether a requirement truly can't be met from
// the team's sandbox, and to word it as a step for the owner. It gives nil
// when the PM can't say, and the PM's name when its seat is busy.
func (lp *Loop) judgeOwnerStep(ctx context.Context, p core.Project, t core.Task, seat core.Role, u core.Unreachable) (*ownerStepJudgement, string) {
	if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
		return nil, ""
	}
	if held, err := lp.holdSeat(ctx, t.ID, seat.Name); err != nil || !held {
		return nil, seat.Name
	}
	dir, err := lp.pmWorkDir()
	if err != nil {
		return nil, ""
	}
	spec := lp.baseSpec(seat, dir, ownerStepPrompt(p, t, u))
	spec.ProjectID, spec.Role = p.ID, core.RolePM
	spec.TaskID = t.ID
	spec.Observer = lp.watchTurn(t, core.RolePM, seat, dir, false)
	lp.withTools(&spec, lp.answerTools(p.ID, seat))
	var judged ownerStepJudgement
	// Retain the obstacle's classification independently of the owner-step
	// recommendation, including across an unsuccessful correction turn.
	var reportedAsset *bool
	_, _, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) error {
		var classification struct {
			AssetCreation *bool `json:"asset_creation"`
		}
		if decodeReply(reply, &classification) == nil && classification.AssetCreation != nil && *classification.AssetCreation {
			reportedAsset = classification.AssetCreation
		}
		judged = ownerStepJudgement{}
		if err := decodeReply(reply, &judged); err != nil {
			return errors.New("the reply was not valid JSON")
		}
		if _, ok := t.Designer(); ok && judged.AssetCreation == nil {
			return errors.New("classify asset_creation as true or false")
		}
		judged.Reason = text.Clip(strings.TrimSpace(judged.Reason), 300)
		if judged.OwnerStep == nil || judged.Reason == "" {
			return errors.New(`the reply needs "owner_step" and a "reason"`)
		}
		return nil
	})
	if err != nil || parseErr != nil {
		if hasProductionDesigner(t) && reportedAsset != nil {
			return &ownerStepJudgement{AssetCreation: reportedAsset}, ""
		}
		return nil, ""
	}
	return &judged, ""
}

// ownerStepPrompt asks the PM to judge a requirement the implementer says
// it can't meet from its sandbox.
func ownerStepPrompt(p core.Project, t core.Task, u core.Unreachable) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the work moving for the project %s. Goal: %s\n\nTask %s: %s\n", p.Title, p.Brief.Goal, t.Label(), text.Clip(t.Objective, 500))
	if len(t.Criteria) > 0 {
		b.WriteString("Its criteria:\n" + numbered(t.Criteria))
	}
	fmt.Fprintf(&b, "\nThe implementer says it can't meet this requirement from its sandbox, which writes only to its workspace and has no general network or access to the owner's machine; browser access is available only when the owner allows it:\n%s\n\nWhy: %s\n", u.Criterion, orDash(u.Why))
	b.WriteString(`
The team can run the project's check through run_check in a daemon-hosted sandbox, with localhost when the project allows it, on any engine. A requirement that the check passes stays with the team; it is not an owner step. QA can use a daemon-hosted app when a run recipe and browser access are available.
Judge whether a requirement the implementer can't meet from its sandbox is truly out of the team's reach, such as a live run on the owner's machine or in their browser, or whether the team could meet it after all, such as with a test or a fake. If it is out of reach, word it as one step the owner checks after the change lands. The owner decides. Only judge: do not change, queue or approve anything.

Reply with only this JSON object:
{"owner_step": true or false, "step": "what the owner checks after it lands, one line", "reason": "one short line the owner reads"}`)
	reply := b.String()
	if hasProductionDesigner(t) {
		reply = strings.ReplaceAll(reply, `"owner_step": true or false`, `"asset_creation": true or false, "owner_step": true or false`)
		reply = strings.ReplaceAll(reply, `"finding": 1, "narrow"`, `"finding": 1, "asset_creation": false, "narrow"`)
	}
	return reply + assetRule(t)
}

// Review reports use the same quoted criterion identity as implementer reports.
func reviewRequirement(p core.Project, t core.Task, f finding) string {
	if f.Criterion == "" {
		f.Criterion = f.Note
	}
	return matchCriterion(append(slices.Clone(t.Criteria), p.Brief.Criteria...), f.Criterion)
}

func reviewReport(p core.Project, t core.Task, f finding, asset *bool) core.Unreachable {
	key := fmt.Sprintf("%d/%d/%d/%s/%s/%s", len(t.Revisions), t.TextVersion, p.Brief.Version, f.Role, f.Criterion, f.Note)
	return core.Unreachable{ID: fmt.Sprintf("review-%x", sha256.Sum256([]byte(key))), Source: "review", Finding: f.Note, Criterion: reviewRequirement(p, t, f), Why: f.Note, Revision: len(t.Revisions), TextVersion: t.TextVersion, BriefVersion: p.Brief.Version, AssetCreation: asset}
}
