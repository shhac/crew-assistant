package app

import (
	"context"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// AssistantInput is what the owner sets of an assistant profile.
type AssistantInput struct {
	Name        string       `json:"name"`
	Personality string       `json:"personality"`
	Model       config.Model `json:"model"`
	// Browser is whether the assistant may use the owner's browser; nil
	// keeps what the profile had.
	Browser *config.Browser `json:"browser,omitempty"`
	// Avatar is a stand-in face for a new profile, such as a suggestion's
	// sketch, with the look to draw; a drawn picture is changed only by
	// drawing again.
	Avatar *config.Avatar `json:"avatar,omitempty"`
}

// CreateAssistant adds an assistant profile and has Codex draw it, as a new
// member is drawn. It doesn't take the seat: the owner chooses who does in
// Settings.
func (a *App) CreateAssistant(ctx context.Context, in AssistantInput) (config.AssistantProfile, error) {
	p := config.AssistantProfile{ID: config.NewProfileID(), Avatar: config.DefaultAvatar(in.Name)}
	if in.Avatar != nil {
		p.Avatar = in.Avatar.Normalized()
		p.Avatar.Image = ""
	}
	in.applyTo(&p)
	err := a.editAssistants(func(next *config.Config) error {
		next.Assistants = append(next.Assistants, p)
		return nil
	})
	if err != nil {
		return config.AssistantProfile{}, err
	}
	a.drawSoon(drawingKey(p.ID), func() error { return a.DrawAssistant(ctx, p.ID, "") })
	return p, nil
}

// SaveAssistant changes an assistant profile. A different model, name or
// personality in the seat starts a new session with the assistant.
func (a *App) SaveAssistant(ctx context.Context, id string, in AssistantInput) (config.AssistantProfile, error) {
	var saved config.AssistantProfile
	err := a.editAssistants(func(next *config.Config) error {
		p := next.ProfileRef(id)
		if p == nil {
			return core.ErrNotFound
		}
		in.applyTo(p)
		if in.Avatar != nil {
			avatar := in.Avatar.Normalized()
			avatar.Image, avatar.Look = p.Avatar.Image, p.Avatar.Look
			p.Avatar = avatar
		}
		saved = *p
		return nil
	})
	if err != nil {
		return config.AssistantProfile{}, err
	}
	return saved, nil
}

// DeleteAssistant removes an assistant profile and what it kept about
// itself. Deleting the one in the seat leaves the seat empty until the owner
// chooses another; the owner's own memories stay for whoever that is.
func (a *App) DeleteAssistant(ctx context.Context, id string) error {
	err := a.editAssistants(func(next *config.Config) error {
		i := slices.IndexFunc(next.Assistants, func(p config.AssistantProfile) bool { return p.ID == id })
		if i < 0 {
			return core.ErrNotFound
		}
		next.Assistants = slices.Delete(next.Assistants, i, i+1)
		if next.Assistant.Seat == id {
			next.Assistant.Seat = ""
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.Core.ForgetAssistant(ctx, id)
}

// editAssistants changes the assistant profiles on a copy, so a refused edit
// leaves the running config as it was.
func (a *App) editAssistants(edit func(*config.Config) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg.CloneAssistants()
	if err := edit(&next); err != nil {
		return err
	}
	return a.updateConfigLocked(next)
}

func (in AssistantInput) applyTo(p *config.AssistantProfile) {
	p.Name, p.Personality = strings.TrimSpace(in.Name), strings.TrimSpace(in.Personality)
	p.Model = in.Model
	p.Model.Model, p.Model.Effort = strings.TrimSpace(p.Model.Model), strings.TrimSpace(p.Model.Effort)
	if in.Browser != nil {
		p.Browser = in.Browser.Trimmed()
	}
}
