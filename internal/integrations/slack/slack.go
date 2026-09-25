// Package slack provides an owner-only DM channel using Slack Socket Mode.
package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/shhac/crew-assistant/internal/lifecycle"
)

type Config struct {
	BotTokenEnv string
	AppTokenEnv string
	OwnerUserID string
}
type Message struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
}

// Inbox must durably claim before acknowledgement. The daemon records completion
// after handling and surfaces unresolved claims after a crash instead of replaying
// potentially effectful owner requests automatically.
type Inbox interface {
	Claim(context.Context, string) (bool, error)
}
type Handler func(context.Context, Message) (string, error)
type Client struct {
	cfg    Config
	inbox  Inbox
	api    *slackapi.Client
	socket *socketmode.Client
}

func New(cfg Config, inbox Inbox) (*Client, error) {
	if cfg.OwnerUserID == "" || cfg.BotTokenEnv == "" || cfg.AppTokenEnv == "" {
		return nil, errors.New("Slack owner user ID and token environment references are required")
	}
	if inbox == nil {
		return nil, errors.New("Slack requires a durable event inbox")
	}
	bot, app := os.Getenv(cfg.BotTokenEnv), os.Getenv(cfg.AppTokenEnv)
	if bot == "" || app == "" {
		return nil, errors.New("Slack bot or app token environment variable is not set")
	}
	// SDK debug output includes protocol payloads; never enable it for private DMs.
	api := slackapi.New(bot, slackapi.OptionAppLevelToken(app), slackapi.OptionLog(log.New(io.Discard, "", 0)))
	socket := socketmode.New(api, socketmode.OptionLog(log.New(io.Discard, "", 0)))
	return &Client{cfg: cfg, inbox: inbox, api: api, socket: socket}, nil
}

// ParseOwnerMessage is a pure allowlist gate. Channel messages, other users,
// bots, edits, shared-channel messages and malformed envelopes cannot invoke the PA.
func ParseOwnerMessage(owner string, event slackevents.EventsAPIEvent) (Message, bool) {
	msg, ok := event.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok || owner == "" || msg.User != owner || msg.ChannelType != "im" || msg.Channel == "" || msg.SubType != "" || msg.BotID != "" || event.IsExtSharedChannel || strings.TrimSpace(msg.Text) == "" {
		return Message{}, false
	}
	callback, ok := event.Data.(*slackevents.EventsAPICallbackEvent)
	if !ok || callback.EventID == "" {
		return Message{}, false
	}
	thread := msg.ThreadTimeStamp
	if thread == "" {
		thread = msg.TimeStamp
	}
	if thread == "" {
		return Message{}, false
	}
	return Message{ID: callback.EventID, Text: msg.Text, Channel: msg.Channel, ThreadTS: thread}, true
}

// transport is how Run reaches Slack, replaced in tests.
type transport struct {
	events <-chan socketmode.Event
	listen func(context.Context) error
	ack    func(context.Context, string)
	reply  func(context.Context, Message, string) error
}

// Run takes the owner's messages while stop.Graceful lasts and answers them
// on stop.Force. Once stopping, nothing more is claimed or acknowledged, so
// Slack delivers it to the next run; messages already claimed are still
// answered before Run returns.
func (c *Client) Run(stop lifecycle.Stop, handler Handler) error {
	return c.run(stop, handler, transport{events: c.socket.Events, listen: c.socket.RunContext, ack: c.ack, reply: c.reply})
}

func (c *Client) run(stop lifecycle.Stop, handler Handler, t transport) error {
	if handler == nil {
		return errors.New("Slack handler is required")
	}
	listening, stopListening := context.WithCancel(stop.Graceful)
	defer stopListening()
	queue := make(chan Message, 64)
	listener := start(func() error { return t.listen(listening) })
	answerer := start(func() error { return answer(stop.Force, queue, handler, t.reply) })
	err := c.intake(stop, listening, queue, t, listener, answerer)
	stopListening()
	close(queue)
	<-answerer.done
	<-listener.done
	if err != nil {
		return err
	}
	return answerer.err
}

// finished is a goroutine's error, readable once done is closed, by as many
// waiters as there are.
type finished struct {
	done chan struct{}
	err  error
}

func start(fn func() error) *finished {
	f := &finished{done: make(chan struct{})}
	go func() {
		defer close(f.done)
		f.err = fn()
	}()
	return f
}

// answer replies to each claimed message in turn, until the queue closes.
func answer(ctx context.Context, queue <-chan Message, handler Handler, reply func(context.Context, Message, string) error) error {
	for m := range queue {
		response, err := handler(ctx, m)
		if err != nil {
			response = "I couldn't complete that request. Its recorded state is available in the dashboard; I have not automatically repeated it."
		}
		if response == "" {
			continue
		}
		if err := reply(ctx, m, response); err != nil {
			return errors.New("Slack reply delivery is uncertain; inspect the recorded conversation before resending")
		}
	}
	return nil
}

// intake claims the owner's messages and queues them for answering, until
// the stop, a failed reply or the socket ending.
func (c *Client) intake(stop lifecycle.Stop, listening context.Context, queue chan<- Message, t transport, listener, answerer *finished) error {
	for {
		select {
		case <-stop.Graceful.Done():
			return nil
		case <-answerer.done:
			return nil
		case <-listener.done:
			if listener.err != nil && !stop.Stopping() {
				return errors.New("Slack Socket Mode stopped; check connectivity and app credentials")
			}
			return nil
		case event := <-t.events:
			// Both cases may be ready at once, and a select picks either.
			if stop.Stopping() {
				return nil
			}
			if event.Type == socketmode.EventTypeInvalidAuth {
				return errors.New("Slack rejected app credentials")
			}
			msg, allowed := c.ownerMessage(event)
			if !allowed {
				if event.Request != nil {
					t.ack(listening, event.Request.EnvelopeID)
				}
				continue
			}
			// Leave a saturated inbox unacknowledged so Slack can retry. Never durably
			// claim a request that cannot be queued in this process.
			if len(queue) == cap(queue) {
				continue
			}
			fresh, err := c.inbox.Claim(stop.Force, msg.ID)
			if err != nil {
				return errors.New("cannot persist Slack event receipt")
			}
			if event.Request != nil {
				t.ack(listening, event.Request.EnvelopeID)
			}
			if fresh {
				queue <- msg
			}
		}
	}
}

// ownerMessage is the owner's direct message an event carries, if it is one.
func (c *Client) ownerMessage(event socketmode.Event) (Message, bool) {
	if event.Type != socketmode.EventTypeEventsAPI {
		return Message{}, false
	}
	payload, ok := event.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return Message{}, false
	}
	return ParseOwnerMessage(c.cfg.OwnerUserID, payload)
}

func (c *Client) ack(ctx context.Context, id string) {
	ackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = c.socket.AckCtx(ackCtx, id, nil)
}

func (c *Client) reply(ctx context.Context, msg Message, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _, err := c.api.PostMessageContext(ctx, msg.Channel, slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(msg.ThreadTS), slackapi.MsgOptionDisableLinkUnfurl(), slackapi.MsgOptionDisableMediaUnfurl())
	if err != nil {
		return errors.New("Slack message delivery failed or is uncertain")
	}
	return nil
}

// Notify sends only to the configured owner. The caller decides when a meaningful
// update warrants notification and records an outbox entry before sending.
func (c *Client) Notify(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("Slack notification is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ch, _, _, err := c.api.OpenConversationContext(ctx, &slackapi.OpenConversationParameters{Users: []string{c.cfg.OwnerUserID}})
	if err != nil {
		return errors.New("cannot open Slack owner DM")
	}
	return c.reply(ctx, Message{Channel: ch.ID}, text)
}
