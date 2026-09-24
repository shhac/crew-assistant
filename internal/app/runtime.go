package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	linearapi "github.com/shhac/crew-assistant/internal/integrations/linear"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
)

// Run owns deterministic supervision. noDispatch is fixed at process boot;
// changing pause or live configuration cannot enable starts or resumes beneath it.
func (a *App) Run(ctx context.Context, noDispatch bool) error {
	ctx, cancel := context.WithCancel(ctx)
	var listeners sync.WaitGroup
	defer func() { cancel(); listeners.Wait() }()
	if a.Demo {
		<-ctx.Done()
		return nil
	}
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		if err := a.RunChatQueue(ctx); err != nil && ctx.Err() == nil {
			a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "chat_queue"}, err)
			a.Status("chat", "Conversation", "error", "The chat stopped; restart crew-assistant to pick up waiting messages")
		}
	}()
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		a.Work.Run(ctx, noDispatch)
	}()
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		a.Work.RunWakes(ctx)
	}()
	pending, err := a.Core.PendingEvents(ctx)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		a.Core.RecordActivity(ctx, "", "recovery.pending", fmt.Sprintf("%d interrupted inbound or outbound operations need inspection. Uncertain effects were not replayed.", len(pending)))
	}
	var slackClient *slackapi.Client
	cfg := a.Config()
	if cfg.Slack.OwnerUserID != "" {
		slackClient, err = slackapi.New(slackapi.Config{BotTokenEnv: cfg.Slack.BotTokenEnv, AppTokenEnv: cfg.Slack.AppTokenEnv, OwnerUserID: cfg.Slack.OwnerUserID}, inbox{a.Core})
		if err != nil {
			a.Status("slack", "Slack bot messaging", "error", err.Error())
		} else {
			a.Status("slack", "Slack bot messaging", "configured", "Starting")
			listeners.Add(1)
			go func() {
				defer listeners.Done()
				err := slackClient.Run(ctx, func(c context.Context, m slackapi.Message) (string, error) {
					result, chatErr := a.Chat(c, m.Text)
					if chatErr != nil {
						return "", chatErr
					}
					if e := a.Core.CompleteEvent(c, "slack:"+m.ID); e != nil {
						return "", e
					}
					return result.Message, nil
				})
				if ctx.Err() == nil && err != nil {
					a.Status("slack", "Slack bot messaging", "error", err.Error())
				}
			}()
		}
	}
	_ = a.SyncLinear(ctx)
	supervise := func() {
		if slackClient != nil {
			a.notify(ctx, slackClient.Notify)
		}
	}
	supervise()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	linearTick := time.NewTicker(5 * time.Minute)
	defer linearTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			supervise()
		case <-linearTick.C:
			_ = a.SyncLinear(ctx)
		}
	}
}

type inbox struct{ s *core.Service }

func (i inbox) Claim(ctx context.Context, id string) (bool, error) {
	return i.s.ClaimEvent(ctx, "slack:"+id)
}

func (a *App) SyncLinear(ctx context.Context) error {
	if a.Demo {
		return nil
	}
	cfg := a.Config()
	cliErr := a.syncCLIConnections(ctx)
	if !cfg.LegacyLinearImportEnabled() {
		return cliErr
	}
	c, err := linearapi.New(linearapi.Config{APIKeyEnv: cfg.Linear.APIKeyEnv, TeamIDs: cfg.Linear.TeamIDs})
	if err == nil {
		err = a.syncLinear(ctx, c)
	}
	if err != nil {
		a.Status("linear", "Linear", "error", err.Error())
	}
	return errors.Join(cliErr, err)
}

type assignmentSource interface {
	Assigned(context.Context) (linearapi.Assignments, error)
}

func (a *App) syncLinear(ctx context.Context, c assignmentSource) error {
	result, err := c.Assigned(ctx)
	if err != nil {
		return err
	}
	for _, issue := range result.Issues {
		description := issue.Description + "\nSource: " + issue.URL + "\nLinear status: " + issue.State.Name
		_, err = a.Core.CreateProject(ctx, core.ProjectInput{Title: issue.Identifier + " · " + issue.Title, SourceDescription: description, SourceID: "linear:" + issue.ID})
		if err != nil {
			return err
		}
	}
	a.Status("linear", "Linear", "connected", fmt.Sprintf("%d assigned issues", len(result.Issues)))
	return nil
}
func (a *App) once(ctx context.Context, key string, fn func() error) error {
	claimed, err := a.Core.ClaimEvent(ctx, key)
	if err != nil || !claimed {
		return err
	}
	if err = fn(); err != nil {
		_ = a.Core.RecordActivity(ctx, "", "operation.interrupted", "An operation needs inspection before any repeat: "+key)
		return err
	}
	return a.Core.CompleteEvent(ctx, key)
}
func (a *App) notify(ctx context.Context, send func(context.Context, string) error) {
	snap, err := a.Core.Snapshot(ctx)
	if err != nil {
		return
	}
	for _, d := range snap.Decisions {
		if d.Status != "open" {
			continue
		}
		key := "notify:decision:" + d.ID
		_ = a.once(ctx, key, func() error {
			return send(ctx, d.Title+"\nRecommendation: "+d.Recommendation+"\n"+d.Context+"\nResolve this decision in the dashboard.")
		})
	}
}
