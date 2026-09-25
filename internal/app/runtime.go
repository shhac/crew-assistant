package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	linearapi "github.com/shhac/crew-assistant/internal/integrations/linear"
	slackapi "github.com/shhac/crew-assistant/internal/integrations/slack"
	"github.com/shhac/crew-assistant/internal/lifecycle"
)

// Run owns deterministic supervision. noDispatch is fixed at process boot;
// changing pause or live configuration cannot enable starts or resumes beneath it.
//
// Work is taken while stop.Graceful lasts: a task step, a chat reply, a Slack
// message, a wake. Work taken runs on stop.Force, so when Graceful ends Run
// returns once that work is done, and when Force ends it is cut short.
func (a *App) Run(stop lifecycle.Stop, noDispatch bool) (runErr error) {
	// Run returns nil only once Graceful has ended; its own failure stops
	// everything it started, as a forced stop would.
	stop, cancel := stop.WithCancel()
	var listeners sync.WaitGroup
	a.setStop(stop)
	defer func() {
		if runErr != nil {
			cancel()
		}
		listeners.Wait()
		a.closeDrawings()
		cancel()
	}()
	if a.Demo {
		<-stop.Graceful.Done()
		return nil
	}
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		if err := a.RunChatQueue(stop); err != nil && !stop.Stopping() {
			a.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "chat_queue"}, err)
			a.Status("chat", "Conversation", "error", "The chat stopped; restart crew-assistant to pick up waiting messages")
		}
	}()
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		a.Work.Run(stop, noDispatch)
	}()
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		a.Work.RunWakes(stop)
	}()
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		a.watchConfig(stop.Graceful)
	}()
	pending, err := a.Core.PendingEvents(stop.Force)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		a.Core.RecordActivity(stop.Force, "", "recovery.pending", fmt.Sprintf("%d interrupted inbound or outbound operations need inspection. Uncertain effects were not replayed.", len(pending)))
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
				err := slackClient.Run(stop, a.answerSlack)
				if !stop.Stopping() && err != nil {
					a.Status("slack", "Slack bot messaging", "error", err.Error())
				}
			}()
		}
	}
	_ = a.SyncLinear(stop.Graceful)
	// Each look runs on Force: claiming a notice and sending it belong
	// together. No look starts once stopping.
	supervise := func() {
		if slackClient != nil && !stop.Stopping() {
			a.notify(stop.Force, slackClient.Notify)
		}
	}
	supervise()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	linearTick := time.NewTicker(5 * time.Minute)
	defer linearTick.Stop()
	for {
		select {
		case <-stop.Graceful.Done():
			return nil
		case <-tick.C:
			supervise()
		case <-linearTick.C:
			_ = a.SyncLinear(stop.Graceful)
		}
	}
}

// answerSlack answers the owner's Slack message through the chat queue. A
// message that arrives as the daemon stops is queued for the next run, and
// the owner is told where the answer will be.
func (a *App) answerSlack(ctx context.Context, m slackapi.Message) (string, error) {
	result, err := a.Chat(ctx, m.Text)
	queued := errors.Is(err, ErrStopping)
	if err != nil && !queued {
		return "", err
	}
	if err := a.Core.CompleteEvent(ctx, "slack:"+m.ID); err != nil {
		return "", err
	}
	if queued {
		return "I'm stopping for now. Your message is queued, and I'll answer it in the dashboard when I'm running again.", nil
	}
	return result.Message, nil
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

// watchConfig takes on the config file whenever it changes on disk, and
// wakes the loop so work held by an old limit is looked at again at once.
func (a *App) watchConfig(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	seen := configStamp(a.configPath)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		stamp := configStamp(a.configPath)
		if stamp == seen {
			continue
		}
		seen = stamp
		changed, err := a.ReloadConfig()
		if err != nil {
			a.Status("config", "Configuration", "error", "The config file changed but couldn't be used: "+err.Error())
			continue
		}
		if changed {
			a.Status("config", "Configuration", "connected", "Reloaded from the config file")
			a.Work.Nudge()
		}
	}
}

// configStamp is what changes when the config file does.
func configStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}
