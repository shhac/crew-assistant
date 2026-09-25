package slack

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/shhac/crew-assistant/internal/lifecycle"
)

type claims struct {
	mu  sync.Mutex
	ids []string
}

func (c *claims) Claim(_ context.Context, id string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
	return true, nil
}

func ownerEvent(id string) socketmode.Event {
	e := event()
	e.Data = &slackevents.EventsAPICallbackEvent{EventID: id}
	return socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: e, Request: &socketmode.Request{EnvelopeID: "envelope-" + id}}
}

// Once stopping, a message already claimed is still answered, and one that
// arrives after is neither claimed nor acknowledged, so Slack delivers it to
// the next run.
func TestAStopAnswersWhatWasClaimedAndClaimsNothingMore(t *testing.T) {
	inbox := &claims{}
	c := &Client{cfg: Config{OwnerUserID: "owner-one"}, inbox: inbox}
	events := make(chan socketmode.Event)
	acked := make(chan string, 8)
	replied := make(chan string, 8)
	release := make(chan struct{})
	var handlerCtx context.Context
	handler := func(ctx context.Context, m Message) (string, error) {
		handlerCtx = ctx
		if m.ID == "first" {
			<-release
		}
		return "answer to " + m.ID, nil
	}
	tr := transport{
		events: events,
		listen: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		ack:    func(_ context.Context, id string) { acked <- id },
		reply:  func(_ context.Context, m Message, text string) error { replied <- text; return nil },
	}
	graceful, stopTaking := context.WithCancel(context.Background())
	stop := lifecycle.Stop{Graceful: graceful, Force: context.Background()}
	done := make(chan error, 1)
	go func() { done <- c.run(stop, handler, tr) }()
	for _, id := range []string{"first", "second"} {
		events <- ownerEvent(id)
		if got := <-acked; got != "envelope-"+id {
			t.Fatalf("acked %s", got)
		}
	}
	stopTaking()
	select {
	case events <- ownerEvent("third"):
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after answering what it had claimed")
	}
	if len(replied) != 2 || <-replied != "answer to first" || <-replied != "answer to second" {
		t.Fatal("a claimed message went unanswered")
	}
	if len(acked) != 0 || len(inbox.ids) != 2 {
		t.Fatalf("a message was taken after the stop: claimed %v", inbox.ids)
	}
	if handlerCtx.Err() != nil {
		t.Fatal("answers ran on a context the stop ended")
	}
}
