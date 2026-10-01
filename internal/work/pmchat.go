package work

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

func (lp *Loop) SendPMChat(ctx context.Context, projectID, id, message string) (core.PMChatMessage, error) {
	m, err := lp.Core.SendPMChat(ctx, projectID, id, message)
	if err == nil {
		lp.Nudge()
	}
	return m, err
}
func (lp *Loop) RetryPMChat(ctx context.Context, projectID, id string) (core.PMChatMessage, error) {
	m, err := lp.Core.RetryPMChat(ctx, projectID, id)
	if err == nil {
		lp.Nudge()
	}
	return m, err
}
func (lp *Loop) answerPMChats(ctx context.Context, snap core.Snapshot, waited bool) ([]<-chan any, error) {
	var started []<-chan any
	for _, p := range snap.Projects {
		pending := false
		for _, m := range snap.PMChats {
			if m.ProjectID == p.ID && m.Status == "waiting" {
				pending = true
				break
			}
		}
		if !pending {
			continue
		}
		if seat, ok := p.PMSeat(); ok {
			if wait, _ := lp.usageWait(ctx, seat); !wait.IsZero() {
				continue
			}
		}
		taken := &slots{lp: lp, forOwner: true}
		m, c, seat, ok, err := lp.Core.ClaimPMChat(ctx, p.ID, taken.admit)
		if err != nil {
			taken.giveBack()
			return started, err
		}
		if !ok {
			continue
		}
		started = append(started, lp.run(ctx, claimed{project: p.ID, token: c.Token, seat: seat}, waited,
			func(ctx context.Context) error {
				return lp.pmChatTurn(context.WithValue(core.FencedProject(ctx, p.ID, c.Token), ownerAskedKey{}, true), p.ID, m, seat)
			},
			func(ctx context.Context) error { return lp.Core.ReleaseProjectClaim(ctx, p.ID, c.Token) }))
	}
	return started, nil
}
func pmChatHistory(snap core.Snapshot, projectID, beforeID string, limit int) string {
	// Replies are appended after queued questions. Select by the question's
	// position, not the reply's position, so an earlier reply is still visible.
	earlier := map[string]bool{}
	for _, m := range snap.PMChats {
		if m.ProjectID != projectID {
			continue
		}
		if m.ID == beforeID {
			break
		}
		if m.From == "owner" && m.Status == "answered" {
			earlier[m.ID] = true
		}
	}
	var messages []core.PMChatMessage
	for _, m := range snap.PMChats {
		if m.ProjectID != projectID || m.From != "owner" || !earlier[m.ID] {
			continue
		}
		messages = append(messages, m)
		for _, reply := range snap.PMChats {
			if reply.ProjectID == projectID && reply.From == "pm" && reply.ReplyTo == m.ID && reply.Status == "answered" {
				messages = append(messages, reply)
			}
		}
	}
	var b strings.Builder
	for _, m := range messages[max(0, len(messages)-limit):] {
		fmt.Fprintf(&b, "%s: %s\n", m.From, text.Clip(m.Text, 1000))
	}
	return b.String()
}
func pmTeamLine(p core.Project) string {
	var names []string
	if p.Playbook != nil {
		for _, r := range p.Playbook.Roles {
			names = append(names, r.Name+" ("+strings.Join(r.Kinds, ", ")+")")
		}
	}
	return "\nTeam: " + strings.Join(names, "; ") + "\n"
}
func (lp *Loop) pmChatTurn(ctx context.Context, projectID string, m core.PMChatMessage, seat core.Role) error {
	snap, err := lp.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	p, ok := findProject(snap, projectID)
	if !ok {
		return core.ErrNotFound
	}
	dir, err := lp.pmWorkDir()
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are the PM of %s. Goal: %s\nAudience: %s\nConstraints: %s\n", p.Title, p.Brief.Goal, p.Brief.Audience, p.Brief.Constraints)
	fmt.Fprintf(&b, "Requirements:\n%s\n", numbered(p.Brief.Criteria))
	b.WriteString(pmToldText(snap, p))
	b.WriteString(pmTeamLine(p))
	pmTasks(&b, snap, p)
	pmTriage(&b, snap, p)
	b.WriteString("\nRecent project conversation:\n" + pmChatHistory(snap, p.ID, m.ID, 20))
	fmt.Fprintf(&b, "\nThe owner asks:\n%s\n\nReply in plain words about this project. You may tidy tasks, link them, queue siblings, add notes and order the queued list with order_tasks. Do not build, direct, stop or land work. Do not send triage tasks on; your next look handles that. Explain what you changed and why. This conversation is owner-initiated even when work is paused; your changes start no work while paused.", m.Text)
	changes := &pmChatChanges{}
	tools := lp.managerTools(p.ID, seat).proposing(p.Playbook)
	tools.chat = changes
	spec := lp.baseSpec(seat, dir, b.String())
	lp.withTools(&spec, tools)
	result, err := lp.runRole(ctx, spec)
	receipt := changes.snapshot()
	if err != nil || strings.TrimSpace(result.Text) == "" {
		reason := "The PM could not finish this reply. Recorded changes were kept."
		if err == nil {
			reason = "The PM returned an empty reply. Recorded changes were kept."
		}
		return lp.Core.FailPMChat(context.WithoutCancel(ctx), p.ID, m.ID, m.Token, reason, receipt)
	}
	if err = lp.Core.AnswerPMChat(ctx, p.ID, m.ID, m.Token, seat.Name, strings.TrimSpace(result.Text), receipt); err != nil {
		return err
	}
	return lp.Core.RecordActivity(ctx, p.ID, "pm.chat", seat.Name+" replied to the owner")
}

type pmChatChanges struct {
	mu    sync.Mutex
	items []core.PMChatChange
}

func (c *pmChatChanges) snapshot() []core.PMChatChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]core.PMChatChange(nil), c.items...)
}
