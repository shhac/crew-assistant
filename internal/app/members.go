package app

import (
	"context"

	"github.com/shhac/crew-assistant/internal/core"
)

// CreateMember adds a member and has Codex draw it. The member is kept even
// when the drawing cannot start; its preset face stands in. A look given
// with its avatar, such as a suggestion's, is what is drawn.
func (a *App) CreateMember(ctx context.Context, in core.MemberInput) (core.Member, error) {
	m, err := a.Core.SaveMember(ctx, "", in)
	if err != nil {
		return m, err
	}
	look := ""
	if in.Avatar != nil {
		look = in.Avatar.Look
	}
	a.drawSoon(m.ID, func() error { return a.DrawMember(ctx, m.ID, look) })
	return m, nil
}
