package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
)

func (a *App) validateSlackProject(ctx context.Context, cfg config.Slack) error {
	if cfg.ProjectID == "" {
		return nil
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return err
	}
	if _, ok := snap.Project(cfg.ProjectID); !ok {
		return errors.New("Slack bot messaging must name an existing project, or use the assistant")
	}
	// A connection can be prepared before its project has a team or PM.
	return nil
}

func (a *App) newSlack(ctx context.Context, cfg config.Slack) (*slackapi.Client, error) {
	if err := a.validateSlackProject(ctx, cfg); err != nil {
		return nil, err
	}
	c, err := slackapi.New(slackapi.Config{BotTokenEnv: cfg.BotTokenEnv, AppTokenEnv: cfg.AppTokenEnv, OwnerUserID: cfg.OwnerUserID, WorkspaceID: cfg.WorkspaceID}, inbox{a.Core})
	if err != nil {
		return nil, err
	}
	if err := c.VerifyWorkspace(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (a *App) finishSlack(ctx context.Context, m slackapi.Message, text string) (string, error) {
	if err := a.Core.CompleteEvent(ctx, "slack:"+m.ID); err != nil {
		return "", err
	}
	return text, nil
}

// answerSlackPM uses the PM's existing queue and project-bound tools. It never
// enters the global assistant conversation or offers the assistant's tools.
func (a *App) answerSlackPM(ctx context.Context, m slackapi.Message, cfg config.Slack) (string, error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	p, ok := snap.Project(cfg.ProjectID)
	if !ok {
		return a.finishSlack(ctx, m, "This connection's project is no longer available. Choose a project in Settings → Connections → Slack bot messaging.")
	}
	if _, ok := p.PMSeat(); !ok {
		return a.finishSlack(ctx, m, "Choose a project manager on "+p.Title+"'s Team page before messaging the project here.")
	}
	// Slack event IDs, rather than attempts, own the queued question. A
	// repeated event cannot queue it twice, even after a crash or a lost reply.
	sum := sha256.Sum256([]byte(cfg.WorkspaceID + ":" + m.ID))
	id := "slack-" + hex.EncodeToString(sum[:])
	conversation := "slack:" + cfg.WorkspaceID + ":" + m.Channel + ":" + m.ThreadTS
	if _, err := a.Work.SendPMChatInConversation(ctx, p.ID, id, m.Text, conversation); err != nil {
		if errors.Is(err, core.ErrConflict) || errors.Is(err, core.ErrChatValidation) {
			return a.finishSlack(ctx, m, "The project manager couldn't accept this message. Check the project's conversation for waiting messages and try again when it has room.")
		}
		return "", err
	}
	return a.waitSlackPM(ctx, m, p.ID, id)
}

func (a *App) waitSlackPM(ctx context.Context, m slackapi.Message, projectID, id string) (string, error) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		messages, err := a.Core.PMChat(ctx, projectID)
		if err != nil {
			return "", err
		}
		for _, msg := range messages {
			if msg.From == "pm" && msg.ReplyTo == id && msg.Status == "answered" {
				return a.finishSlack(ctx, m, msg.Text)
			}
			if msg.ID == id {
				if msg.Status == "failed" {
					return a.finishSlack(ctx, m, "The project manager couldn't finish this reply. Recorded changes were kept; inspect the project's conversation before retrying.")
				}
				if msg.Status == "waiting" && a.Stopping() {
					return a.finishSlack(ctx, m, "I'm stopping for now. Your message is queued for the project manager; its answer will appear in the project's conversation when I'm running again.")
				}
				if msg.Status == "waiting" {
					snap, err := a.Core.Snapshot(ctx)
					if err != nil {
						return "", err
					}
					for _, p := range snap.Projects {
						if p.ID != projectID {
							continue
						}
						if seat, ok := p.PMSeat(); ok {
							pause, paused, err := a.Work.EnginePause(ctx, seat.Engine)
							if err != nil {
								return "", err
							}
							if paused {
								end := "until you resume"
								if pause.Until != nil {
									end = "until " + pause.Until.Local().Format("Mon 15:04")
								}
								return a.finishSlack(ctx, m, "Your message is queued for the project manager. "+config.EngineLabel(seat.Engine)+" is paused "+end+"; its answer will appear in the project's conversation after it resumes.")
							}
						}
					}
				}
				if msg.Status == "waiting" && a.dispatchDisabled.Load() {
					return a.finishSlack(ctx, m, "Your message is queued for the project manager. Work dispatch is paused; its answer will appear in the project's conversation after dispatch resumes.")
				}
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}
