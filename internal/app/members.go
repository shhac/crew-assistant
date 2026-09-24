package app

import (
	"context"

	"github.com/shhac/crew-assistant/internal/core"
)

// CreateMember adds a member and has Codex draw it. The member is kept even
// when the drawing cannot start; its preset face stands in.
func (a *App) CreateMember(ctx context.Context, in core.MemberInput) (core.Member, error) {
	m, err := a.Core.SaveMember(ctx, "", in)
	if err != nil {
		return m, err
	}
	a.drawSoon(m.ID, func() error { return a.DrawMember(ctx, m.ID, "") })
	return m, nil
}
