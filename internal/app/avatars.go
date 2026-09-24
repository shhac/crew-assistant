package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/avatars"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
)

var kindWords = map[string]string{
	core.RoleImplementer: "the implementer, who writes the work",
	core.RoleReviewer:    "a reviewer, who checks the work carefully",
	core.RoleQA:          "QA, who runs the checks",
}

// painter draws with Codex. Its session has a Codex home of its own, so a
// drawing never shares a runtime with the team's turns.
func (a *App) painterFor() avatars.Painter {
	if a.Painter != nil {
		return a.Painter
	}
	// Tests never reach a real service; one that forgot a painter fails.
	if testing.Testing() {
		return refusePainter{}
	}
	cfg := a.Config()
	state := a.Core.StateDirectory()
	return avatars.CodexPainter{Runner: roles.Native{}, Spec: roles.Spec{
		Binary: cfg.Model.CodexBin, Home: cfg.Model.CodexHome,
		RuntimeHome: filepath.Join(state, "roles", "painter"),
		WorkDir:     filepath.Join(state, "roles", "painter-work"),
	}}
}

// startDrawing draws a character in the background, one picture at a time,
// and hands the stored picture to done. A drawing takes minutes, so the
// dashboard shows it as under way and then the picture or what went wrong.
func (a *App) startDrawing(ctx context.Context, key, character string, done func(context.Context, string) error) error {
	if a.Demo {
		return errors.New("demo mode doesn't draw; start without --demo to draw with Codex")
	}
	if a.Core.DrawingOf(key).Busy {
		return fmt.Errorf("already drawing: %w", core.ErrConflict)
	}
	// The owner's usage limit applies to Codex, the painter unless one is set;
	// reading it would reach Codex itself, which tests never do.
	if a.Painter == nil && !testing.Testing() {
		if wait, detail := a.Work.UsageWait(ctx, "codex"); !wait.IsZero() {
			return errors.New(detail)
		}
	}
	a.Core.SetDrawing(key, core.Drawing{Busy: true})
	a.drawings.Add(1)
	go func() {
		defer a.drawings.Done()
		ctx, cancel := context.WithTimeout(a.lifetime(), 12*time.Minute)
		defer cancel()
		a.paint.Lock()
		defer a.paint.Unlock()
		data, err := a.painterFor().Paint(ctx, character)
		image := ""
		if err == nil {
			image, err = a.Avatars().Put(data)
		}
		if err == nil {
			err = done(ctx, image)
		}
		failure := ""
		if err != nil {
			failure = "Couldn't draw it: " + err.Error()
		}
		a.Core.SetDrawing(key, core.Drawing{Error: failure})
	}()
	return nil
}

// DrawMember draws a team member's face, from look if given, or else from
// how it was last described; with neither, Codex designs one to suit them.
func (a *App) DrawMember(ctx context.Context, id, look string) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	i := -1
	for j, m := range snap.Members {
		if m.ID == id {
			i = j
		}
	}
	if i < 0 {
		return core.ErrNotFound
	}
	m := snap.Members[i]
	look = strings.TrimSpace(look)
	if look == "" {
		look = m.Avatar.Look
	}
	if len(look) > 600 {
		return errors.New("a look is at most 600 characters")
	}
	character := m.Name + ", " + kindWords[m.Kind] + " on a small software team."
	if m.Instructions != "" {
		character += " How they work: " + text.Clip(m.Instructions, 200)
	}
	character += " " + lookOrChoose(look)
	return a.startDrawing(ctx, m.ID, character, func(ctx context.Context, image string) error {
		return a.Core.SetMemberPicture(ctx, m.ID, image, look)
	})
}

// DrawAssistant draws the assistant's own face and puts it in the config.
func (a *App) DrawAssistant(ctx context.Context, look string) error {
	cfg := a.Config().Assistant
	look = strings.TrimSpace(look)
	if look == "" {
		look = cfg.Avatar.Look
	}
	if len(look) > 600 {
		return errors.New("a look is at most 600 characters")
	}
	character := cfg.Name + ", a calm personal assistant who runs projects for its owner. Personality: " + text.Clip(cfg.Personality, 200) + " " + lookOrChoose(look)
	return a.startDrawing(ctx, core.DrawingAssistant, character, func(_ context.Context, image string) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		next := a.cfg
		next.Assistant.Avatar.Image, next.Assistant.Avatar.Look = image, look
		return a.updateConfigLocked(next)
	})
}

func lookOrChoose(look string) string {
	if look == "" {
		return "Design their look yourself to suit them."
	}
	return "Their look: " + look
}

// CreateMember adds a member and has Codex draw it. The member is kept even
// when the drawing cannot start; its preset face stands in.
func (a *App) CreateMember(ctx context.Context, in core.MemberInput) (core.Member, error) {
	m, err := a.Core.SaveMember(ctx, "", in)
	if err != nil || a.Demo {
		return m, err
	}
	if drawErr := a.DrawMember(ctx, m.ID, ""); drawErr != nil {
		a.Core.SetDrawing(m.ID, core.Drawing{Error: "Couldn't draw it: " + drawErr.Error()})
	}
	return m, nil
}

type refusePainter struct{}

func (refusePainter) Paint(context.Context, string) ([]byte, error) {
	return nil, errors.New("tests do not draw with Codex; set a painter")
}

// WaitForDrawings returns once every drawing under way has finished.
func (a *App) WaitForDrawings() { a.drawings.Wait() }

// setLife ties drawings to the daemon's run, so stopping it stops Codex too.
func (a *App) setLife(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.life = ctx
}

func (a *App) lifetime() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.life == nil {
		return context.Background()
	}
	return a.life
}
