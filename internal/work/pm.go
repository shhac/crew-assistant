package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// managePM gives the team's PM one look at a project's to-do list after
// something changed it: work queued, planned or finished. The PM sets the
// order work starts in and what waits for what; it directs no one, and the
// owner's or the assistant's order expresses the owner's priorities. A project with no PM
// sends what waits in triage straight on, so nothing waits on a missing role.
//
// The PM's look is a step of the project, claimed as a task's steps are: it
// takes the PM's seat, which is busy meanwhile for anything else the seat
// holds, and a turn on its engine; it runs in a goroutine of its own, fenced
// by its claim; and a restart reclaims it rather than run it beside one
// still going. It returns the looks it started, and whether it changed the
// record itself.
func (lp *Loop) managePM(ctx context.Context, snap core.Snapshot, waited bool) ([]<-chan any, bool, error) {
	var started []<-chan any
	for _, p := range snap.Projects {
		if p.Paused {
			continue
		}
		seat, ok := p.PMSeat()
		if !ok && snap.HasTriage(p.ID) {
			if err := lp.Core.ReleaseTriage(ctx, p.ID, "the team has no PM"); err != nil {
				return started, true, err
			}
			return started, true, lp.Core.SkipPM(ctx, p.ID, "")
		}
		if !p.PMDue || pmAsking(snap, p.ID) || len(p.Claims) > 0 {
			continue
		}
		if !ok {
			return started, true, lp.Core.SkipPM(ctx, p.ID, "")
		}
		if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
			continue
		}
		// The PM's look waits for its seat and a free turn on its engine
		// rather than hold up the rest of the work waiting for them.
		taken := &slots{lp: lp}
		c, seat, ok, err := lp.Core.ClaimPM(ctx, p.ID, taken.admit)
		if err != nil {
			taken.giveBack()
			return started, false, err
		}
		if !ok {
			continue
		}
		id, token := p.ID, c.Token
		started = append(started, lp.run(ctx, claimed{project: id, token: token, seat: seat}, waited,
			func(ctx context.Context) error { return lp.pmTurn(core.FencedProject(ctx, id, token), id, seat) },
			func(ctx context.Context) error { return lp.Core.ReleaseProjectClaim(ctx, id, token) }))
	}
	return started, false, nil
}

// pmAsking says whether the PM is waiting on the owner's answer, which
// brings it back anyway; looking again before then would only ask again.
func pmAsking(snap core.Snapshot, projectID string) bool {
	return slices.ContainsFunc(snap.Decisions, func(d core.Decision) bool {
		return d.ProjectID == projectID && d.Kind == core.DecisionPMQuestion && d.Status == core.DecisionOpen
	})
}

// pmTurn is the PM's look at the list as it stands when the look starts.
func (lp *Loop) pmTurn(ctx context.Context, projectID string, seat core.Role) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.ErrNotFound
	}
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := pmPrompt(snap, p)
	// The PM reads only what its prompt carries: no repository, no writing.
	// Its tools tidy tasks, link them and queue new ones.
	spec := lp.baseSpec(seat, dir, base)
	lp.withTools(&spec, lp.managerTools(p.ID, seat).proposing(p.Playbook))
	var answer core.PMAnswer
	var questions []string
	_, _, parseErr, err := lp.askForJSON(ctx, spec, func(reply string) (err error) {
		answer, questions, err = parsePM(reply)
		return err
	})
	if err != nil {
		return lp.skipPM(ctx, p.ID, text.Clip(err.Error(), 300))
	}
	if parseErr != nil {
		return lp.skipPM(ctx, p.ID, "its reply could not be read")
	}
	answer.SeenOrderedBy, answer.SeenOrderedAt = p.OrderedBy, p.OrderedAt
	if _, err := lp.Core.ApplyPM(ctx, p.ID, answer); err != nil {
		return err
	}
	return lp.askPMQuestions(ctx, p, seat, questions)
}

// skipPM goes on without the PM when it could not look: the list stays as
// it is, and what waited in triage goes on to the team rather than wait on
// a PM that failed.
func (lp *Loop) skipPM(ctx context.Context, projectID, why string) error {
	if err := lp.Core.SkipPM(ctx, projectID, why); err != nil {
		return err
	}
	// Shutting down is not the PM failing: triage waits for its next look.
	if err := ctx.Err(); err != nil {
		return err
	}
	return lp.Core.ReleaseTriage(ctx, projectID, "the PM couldn't look: "+why)
}

// askPMQuestions brings what the PM couldn't settle about the order to the
// owner.
func (lp *Loop) askPMQuestions(ctx context.Context, p core.Project, seat core.Role, questions []string) error {
	if len(questions) == 0 {
		return nil
	}
	_, err := lp.Core.AskForPM(ctx, p.ID, core.DecisionInput{
		Title:          fmt.Sprintf("%s has questions about the order of work in %s", seat.Name, p.Title),
		Context:        strings.TrimSpace(numbered(questions)),
		Recommendation: "Answer, or let the PM use its judgment",
		Choices:        []string{"Use your judgment", "Keep the order as it is"},
	})
	return err
}

// maxPMAnswers is how many of its latest answered questions the PM sees on
// every look.
const maxPMAnswers = 5

// pmToldText is what the owner has told the PM: anything given for this
// look, and its latest answered questions about the project. An answer
// outlives the look it arrived for, so the PM never asks it again.
func pmToldText(snap core.Snapshot, p core.Project) string {
	var b strings.Builder
	if p.PMDirection != "" {
		fmt.Fprintf(&b, "\nThe owner told you: %s\n", p.PMDirection)
	}
	var answered []core.Decision
	for _, d := range snap.Decisions {
		if d.ProjectID == p.ID && d.Kind == core.DecisionPMQuestion && d.Status == core.DecisionResolved && d.Answer != "" {
			answered = append(answered, d)
		}
	}
	if len(answered) == 0 {
		return b.String()
	}
	slices.SortFunc(answered, func(x, y core.Decision) int { return y.CreatedAt.Compare(x.CreatedAt) })
	b.WriteString("\nYour latest questions to the owner about this project, newest first, with their answers. Don't ask again what they have answered:\n")
	for _, d := range answered[:min(len(answered), maxPMAnswers)] {
		fmt.Fprintf(&b, "- You asked: %s\n  The owner answered: %s\n", text.Clip(strings.Join(strings.Fields(d.Context), " "), 600), text.Clip(d.Answer, 600))
	}
	return b.String()
}

// pmPrompt is everything the PM needs to order the list: the brief, every
// unfinished task with its plan and what it waits for, and who set the order.
func pmPrompt(snap core.Snapshot, p core.Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the to-do list for the project %s. Goal: %s\n", p.Title, p.Brief.Goal)
	b.WriteString(`
The order is yours to set directly. Never ask the owner to approve or confirm an order. Ask only for decisions only the owner can make, such as conflicting priorities in the brief or their words. Decide the order the queued tasks start in, and what each unfinished task has to wait for. A task waits for another when it builds on what the other will change; without stacking, a task never starts before what it waits for has landed. Put first what unblocks the most, then what the owner most needs. Keep the tasks themselves in order too: where a title or requirements are messy, tidy them; where a task should be split or needs a sibling, queue it; where one task depends on another, link them. Do not plan or build anything yourself, and do not direct, stop or land anyone's work.
`)
	switch p.OrderedBy {
	case core.OrderedByOwner, core.OrderedByAssistant:
		fmt.Fprintf(&b, "\nThe %s set the current order; treat it as the owner's priorities. Keep it unless you have a concrete reason to change it, such as a dependency, new work or what unblocks the most. Never simply move back what they moved. Give the reason in note.\n", p.OrderedBy)
	}
	if history := pmChatHistory(snap, p.ID, "", 10); history != "" {
		b.WriteString("\nRecent conversation with the owner; keep agreed priorities in mind:\n" + history)
	}
	b.WriteString(pmToldText(snap, p))
	pmTasks(&b, snap, p)
	if pmTriage(&b, snap, p) {
		b.WriteString(`
Tasks in triage are new work from the owner or the assistant, waiting for you before the team takes them. For each one: tidy its title and requirements with edit_task so the researcher starts from a clear ask, and link it with link_tasks where it depends on or relates to other work. Then send it on to the team, "to": "research", which sends it towards the to-do list, where the order below may place it; or, only when you cannot shape it without the owner, keep it in triage and ask them, "to": "owner", with the question. If To do is full, a task sent on waits for room in Triage and its id is ignored in the order until it joins the queue. A task left out stays in triage until you next look.
`)
	}
	if pmPRChoices(&b, snap, p) {
		b.WriteString(`
The owner turned pull requests off for this project. Each task above under "Started with pull requests" began with them: choose whether it carries on with them as it started ("keep": true), or lands the project's way from here ("keep": false), which closes its pull request, if one is open, and decides again before it lands. Keep one far along with its reviewers; move one that hasn't reached its pull request yet, or whose pull request has nothing worth keeping.
`)
	}
	b.WriteString(`
Name each task by one id: its readable id, such as CA-3, or its canonical id. Reply with only this JSON object:
{"triage": [{"task": "id of a task in triage", "to": "research or owner", "question": "for the owner only: what you need them to decide"}], "order": ["every queued task id, in the order they should start, with any you send on from triage"], "depends": [{"task": "id", "on": ["ids it must wait for; the full list, replacing what it has"]}], "pull_requests": [{"task": "id of a task that started with pull requests", "keep": true}], "note": "one line on what you changed and why", "questions": ["only decisions the owner must make, such as conflicting priorities; never approval of an order"]}`)
	return b.String()
}

// pmPRChoices lists the tasks whose pull requests the PM is to choose
// about, and reports whether there are any.
func pmPRChoices(b *strings.Builder, snap core.Snapshot, p core.Project) bool {
	found := false
	for _, id := range p.PRChoices {
		t, ok := findTask(snap, p.ID, id)
		if !ok || t.Finished() {
			continue
		}
		if !found {
			b.WriteString("\nStarted with pull requests:\n")
			found = true
		}
		fmt.Fprintf(b, "- %s (%s, on the board in %s): %s\n", t.Label(), t.Status, t.Stage, text.Clip(t.Objective, 300))
		if t.PROpen() {
			fmt.Fprintf(b, "  its pull request #%d is open\n", t.Proposal.Number)
		}
	}
	return found
}

// pmTasks is the list as the PM keeps it: every unfinished task, queued ones
// in their current order, with its plan and what it waits for.
func pmTasks(b *strings.Builder, snap core.Snapshot, p core.Project) {
	b.WriteString("\nUnfinished tasks, queued ones in their current order:\n")
	for _, t := range snap.Tasks {
		if t.ProjectID != p.ID || t.Finished() || t.Status == core.TaskTriage {
			continue
		}
		fmt.Fprintf(b, "- %s (%s): %s\n", t.Label(), t.Status, text.Clip(t.Objective, 300))
		if t.Plan != nil {
			fmt.Fprintf(b, "  plan: %s\n", text.Clip(t.Plan.Summary, 400))
			if len(t.Plan.Changes) > 0 {
				fmt.Fprintf(b, "  changes: %s\n", text.Clip(strings.Join(t.Plan.Changes, "; "), 400))
			}
		}
		for _, line := range blockerLines(t) {
			fmt.Fprintf(b, "  %s\n", line)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(b, "  waits for: %s\n", waitsLine(snap.Tasks, t))
		}
	}
}

// pmTriage lists the tasks waiting in triage, with their requirements, and
// reports whether there are any.
func pmTriage(b *strings.Builder, snap core.Snapshot, p core.Project) bool {
	found := false
	for _, t := range snap.Tasks {
		if t.ProjectID != p.ID || t.Status != core.TaskTriage || t.SentOn {
			continue
		}
		if !found {
			b.WriteString("\nIn triage, oldest first:\n")
			found = true
		}
		fmt.Fprintf(b, "- %s: %s\n", t.Label(), text.Clip(t.Objective, 300))
		for _, c := range t.Criteria {
			fmt.Fprintf(b, "  requirement: %s\n", text.Clip(c, 300))
		}
		for _, line := range blockerLines(t) {
			fmt.Fprintf(b, "  %s\n", line)
		}
		if len(t.DependsOn) > 0 {
			fmt.Fprintf(b, "  waits for: %s\n", waitsLine(snap.Tasks, t))
		}
		if t.Answered {
			b.WriteString("  the owner has answered your question about it\n")
		}
	}
	return found
}

// waitsLine is what a task waits for, marking what the owner set: the team
// leaves that in place, so a PM that leaves it out changes nothing. Each is
// named by its readable ID too, where it is among tasks.
func waitsLine(tasks []core.Task, t core.Task) string {
	parts := make([]string, len(t.DependsOn))
	for i, id := range t.DependsOn {
		parts[i] = id
		if j := slices.IndexFunc(tasks, func(o core.Task) bool { return o.ID == id }); j >= 0 {
			parts[i] = tasks[j].Label()
		}
		if t.HeldByOwner(core.RelationDependsOn, id) {
			parts[i] += " (set by the owner)"
		}
	}
	return strings.Join(parts, ", ")
}

// AskPM puts the assistant's question to a project's PM and gives its
// answer. The PM answers from what it keeps, the list with each task's plan
// and what waits for what, so the assistant needn't carry any of it; asked
// this way, it changes nothing.
func (lp *Loop) AskPM(ctx context.Context, projectID, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("ask the PM something")
	}
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return "", core.ErrNotFound
	}
	seat, ok := p.PMSeat()
	if !ok {
		return "", fmt.Errorf("%s has no PM; read_task looks at a task directly: %w", p.Title, core.ErrConflict)
	}
	if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
		return "", fmt.Errorf("%s is holding back for its usage allowance until %s: %w", seat.Name, wait.Format("15:04"), core.ErrConflict)
	}
	dir := filepath.Join(lp.Core.StateDirectory(), "roles", "pm-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You keep the to-do list for the project %s. Goal: %s\n", p.Title, p.Brief.Goal)
	pmTasks(&b, snap, p)
	pmTriage(&b, snap, p)
	fmt.Fprintf(&b, "\nThe owner's assistant asks you:\n\n%s\n\nAnswer in a few plain sentences from what you know of the list. You change nothing by answering; say what you would change, if anything, and why.", question)
	spec := lp.baseSpec(seat, dir, b.String())
	lp.withTools(&spec, lp.answerTools(p.ID, seat))
	// The PM answers from its seat, which works on one thing at a time, and
	// within its engine's bound on turns at once. The chat asking never
	// waits for either: a busy PM is said to be busy, to ask again shortly.
	taken := &slots{lp: lp, forOwner: true}
	c, held, ok, err := lp.Core.ClaimPMQuestion(ctx, p.ID, taken.admit)
	if err != nil {
		taken.giveBack()
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s is busy with other work right now; ask again shortly: %w", seat.Name, core.ErrConflict)
	}
	defer func() {
		taken.giveBack()
		_ = lp.Core.ReleaseProjectClaim(context.WithoutCancel(ctx), p.ID, c.Token)
	}()
	ctx = context.WithValue(context.WithValue(core.FencedProject(ctx, p.ID, c.Token), slotKey{}, held.Engine), ownerAskedKey{}, true)
	result, err := lp.runRole(ctx, spec)
	if err != nil {
		return "", err
	}
	answer := text.Clip(strings.TrimSpace(result.Text), 4000)
	if answer == "" {
		return "", fmt.Errorf("%s gave no answer", seat.Name)
	}
	_ = lp.Core.RecordActivity(ctx, p.ID, "pm.asked", fmt.Sprintf("The assistant asked %s: %s. %s", seat.Name, text.Clip(question, 200), text.Clip(answer, 300)))
	return answer, nil
}

// parsePM reads the PM's JSON answer, tolerating a fenced block or prose.
func parsePM(reply string) (core.PMAnswer, []string, error) {
	var in struct {
		Triage []struct {
			Task     string `json:"task"`
			To       string `json:"to"`
			Question string `json:"question"`
		} `json:"triage"`
		Order   []string `json:"order"`
		Depends []struct {
			Task string   `json:"task"`
			On   []string `json:"on"`
		} `json:"depends"`
		PullRequests []struct {
			Task string `json:"task"`
			Keep *bool  `json:"keep"`
		} `json:"pull_requests"`
		Note      string   `json:"note"`
		Questions []string `json:"questions"`
	}
	if err := decodeReply(reply, &in); err != nil {
		return core.PMAnswer{}, nil, errors.New("the reply was not valid JSON")
	}
	answer := core.PMAnswer{Order: in.Order, Depends: map[string][]string{}, Note: text.Clip(strings.TrimSpace(in.Note), 300)}
	for _, d := range in.Depends {
		answer.Depends[strings.TrimSpace(d.Task)] = d.On
	}
	for _, r := range in.Triage {
		answer.Triage = append(answer.Triage, core.TriageRelease{Task: strings.TrimSpace(r.Task), To: strings.ToLower(strings.TrimSpace(r.To)), Question: strings.TrimSpace(r.Question)})
	}
	for _, c := range in.PullRequests {
		if c.Keep != nil {
			answer.PRFlow = append(answer.PRFlow, core.PRFlowChoice{Task: strings.TrimSpace(c.Task), Keep: *c.Keep})
		}
	}
	return answer, listed(in.Questions, 5), nil
}
