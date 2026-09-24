package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/avatars"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	"github.com/shhac/crew-assistant/internal/text"
)

var kindWords = map[string]string{
	core.RoleImplementer: "the implementer, who writes the work",
	core.RoleReviewer:    "a reviewer, who checks the work carefully",
	core.RoleQA:          "QA, who runs the checks",
}

// CodexPainter draws with Codex, as the config says at the time of drawing.
func (a *App) CodexPainter() avatars.Painter { return codexPainter{a} }

// codexPainter's session has a Codex home of its own, so a drawing never
// shares a runtime with the team's turns.
type codexPainter struct{ a *App }

func (p codexPainter) Paint(ctx context.Context, character string) ([]byte, error) {
	binary, home := p.a.Config().Model.EngineBinary("codex")
	state := p.a.Core.StateDirectory()
	return avatars.CodexPainter{Runner: roles.Native{}, Spec: roles.Spec{
		Binary: binary, Home: home,
		RuntimeHome: filepath.Join(state, "roles", "painter"),
		WorkDir:     filepath.Join(state, "roles", "painter-work"),
	}}.Paint(ctx, character)
}

// Ready holds a drawing to the owner's usage limit on Codex, as a team's
// turn is held.
func (p codexPainter) Ready(ctx context.Context) error {
	if wait, detail := p.a.Work.UsageWait(ctx, "codex"); !wait.IsZero() {
		return errors.New(detail)
	}
	return nil
}

// startDrawing draws a character in the background, one picture at a time,
// and hands the stored picture to done. A drawing takes minutes, so the
// dashboard shows it as under way and then the picture or what went wrong.
func (a *App) startDrawing(ctx context.Context, key, character string, done func(context.Context, string) error) error {
	if a.Painter == nil {
		return errors.New("drawing needs Codex, which demo mode doesn't use")
	}
	if gate, ok := a.Painter.(avatars.Gate); ok {
		if err := gate.Ready(ctx); err != nil {
			return err
		}
	}
	if err := a.markDrawing(key); err != nil {
		return err
	}
	go func() {
		defer a.drawings.Done()
		ctx, cancel := context.WithTimeout(a.lifetime(), 12*time.Minute)
		defer cancel()
		a.paint.Lock()
		defer a.paint.Unlock()
		a.setDrawing(key, finished(a.draw(ctx, character, done)))
	}()
	return nil
}

// draw paints a character, stores the picture and hands it to done.
func (a *App) draw(ctx context.Context, character string, done func(context.Context, string) error) error {
	data, err := a.Painter.Paint(ctx, character)
	if err != nil {
		return err
	}
	image, err := a.Avatars().Put(data)
	if err != nil {
		return err
	}
	return done(ctx, image)
}

// drawSoon starts a drawing that is a side effect of something else, such
// as adding a member; when it cannot start, that is shown as its status. A
// conflict means one is already under way, whose status stands.
func (a *App) drawSoon(key string, start func() error) {
	err := start()
	if err == nil || errors.Is(err, core.ErrConflict) {
		return
	}
	a.setDrawing(key, finished(err))
}

// drawingAssistant keys the assistant's own picture in the drawing status.
const drawingAssistant = "assistant"

type drawing struct {
	busy    bool
	failure string
}

func finished(err error) drawing {
	if err == nil {
		return drawing{}
	}
	return drawing{failure: "Couldn't draw it: " + err.Error()}
}

func (a *App) drawingOf(key string) drawing {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.drawing[key]
}

// markDrawing claims a picture for one drawing; checking and claiming under
// one lock means two requests never both start one.
func (a *App) markDrawing(key string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.drawing[key].busy {
		return fmt.Errorf("already drawing: %w", core.ErrConflict)
	}
	a.drawing[key] = drawing{busy: true}
	a.drawings.Add(1)
	return nil
}

func (a *App) setDrawing(key string, d drawing) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.drawing[key] = d
}

// DrawMember draws a team member's face, from look if given, or else from
// how it was last described; with neither, Codex designs one to suit them.
func (a *App) DrawMember(ctx context.Context, id, look string) error {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	m, ok := snap.Member(id)
	if !ok {
		return core.ErrNotFound
	}
	look, err = resolveLook(look, m.Avatar.Look)
	if err != nil {
		return err
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
	look, err := resolveLook(look, cfg.Avatar.Look)
	if err != nil {
		return err
	}
	character := cfg.Name + ", a calm personal assistant who runs projects for its owner. Personality: " + text.Clip(cfg.Personality, 200) + " " + lookOrChoose(look)
	return a.startDrawing(ctx, drawingAssistant, character, func(_ context.Context, image string) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		next := a.cfg
		next.Assistant.Avatar.Image, next.Assistant.Avatar.Look = image, look
		return a.updateConfigLocked(next)
	})
}

// resolveLook is the look to draw from: the one given, or else the last.
func resolveLook(given, last string) (string, error) {
	look := strings.TrimSpace(given)
	if look == "" {
		look = last
	}
	if len(look) > config.MaxLook {
		return "", fmt.Errorf("a look is at most %d characters", config.MaxLook)
	}
	return look, nil
}

func lookOrChoose(look string) string {
	if look == "" {
		return "Design their look yourself to suit them."
	}
	return "Their look: " + look
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
