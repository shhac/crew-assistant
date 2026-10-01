package slack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/shhac/crew-assistant/internal/lifecycle"
)

// onceInbox claims each event id once, as the durable inbox does, or fails
// every claim when fail is set.
type onceInbox struct {
	mu     sync.Mutex
	seen   map[string]bool
	claims []string
	fail   error
}

func (c *onceInbox) Claim(_ context.Context, id string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail != nil {
		return false, c.fail
	}
	c.claims = append(c.claims, id)
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	fresh := !c.seen[id]
	c.seen[id] = true
	return fresh, nil
}

func (c *onceInbox) claimed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.claims...)
}

func strangerEvent(id string) socketmode.Event {
	e := ownerEvent(id)
	inner := event()
	inner.Data = &slackevents.EventsAPICallbackEvent{EventID: id}
	inner.InnerEvent.Data.(*slackevents.MessageEvent).User = "someone-else"
	e.Data = inner
	return e
}

// intakeRun drives intake alone, with an answerer and listener that never
// finish, so only the events and the stop decide when it returns.
type intakeRun struct {
	events chan socketmode.Event
	acked  chan string
	queue  chan Message
	stop   context.CancelFunc
	done   chan error
}

func startIntake(c *Client, queue chan Message) *intakeRun {
	r := &intakeRun{events: make(chan socketmode.Event), acked: make(chan string, 8), queue: queue, done: make(chan error, 1)}
	graceful, stop := context.WithCancel(context.Background())
	r.stop = stop
	tr := transport{events: r.events, ack: func(_ context.Context, id string) { r.acked <- id }}
	idle := &finished{done: make(chan struct{})}
	go func() {
		r.done <- c.intake(lifecycle.Stop{Graceful: graceful, Force: context.Background()}, graceful, queue, tr, idle, idle)
	}()
	return r
}

// finish stops intake and returns what it returned.
func (r *intakeRun) finish(t *testing.T) error {
	t.Helper()
	r.stop()
	return returnsSoon(t, r.done)
}

func TestAnEventFromSomeoneElseIsAckedButNeverTaken(t *testing.T) {
	inbox := &onceInbox{}
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}, inbox: inbox}
	r := startIntake(c, make(chan Message, 4))

	r.events <- strangerEvent("stranger")
	if got := <-r.acked; got != "envelope-stranger" {
		t.Fatalf("acked %s", got)
	}
	r.events <- socketmode.Event{Type: socketmode.EventTypeHello, Request: &socketmode.Request{EnvelopeID: "envelope-hello"}}
	if got := <-r.acked; got != "envelope-hello" {
		t.Fatalf("acked %s", got)
	}
	if err := r.finish(t); err != nil {
		t.Fatal(err)
	}
	if got := inbox.claimed(); len(got) != 0 {
		t.Fatalf("an event not from the owner was claimed: %v", got)
	}
	if len(r.queue) != 0 {
		t.Fatalf("an event not from the owner was queued for an answer: %v", <-r.queue)
	}
}

func TestRejectedCredentialsEndTheRun(t *testing.T) {
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}, inbox: &onceInbox{}}
	events := make(chan socketmode.Event, 1)
	handled := make(chan Message, 1)
	handler := func(_ context.Context, m Message) (string, error) { handled <- m; return "answer", nil }
	done := make(chan error, 1)
	go func() {
		done <- c.run(lifecycle.Now(context.Background()), handler, quietTransport(events, func(context.Context, Message, string) error { return nil }))
	}()
	events <- socketmode.Event{Type: socketmode.EventTypeInvalidAuth}
	err := returnsSoon(t, done)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err %v, want the credentials refusal", err)
	}
	if len(handled) != 0 {
		t.Fatal("something was answered")
	}
}

// Slack retries an unacknowledged event, so a full queue must neither claim
// nor ack what it can't hold.
func TestAFullQueueLeavesTheEventForSlackToRetry(t *testing.T) {
	inbox := &onceInbox{}
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}, inbox: inbox}
	queue := make(chan Message, 1)
	queue <- Message{ID: "already-waiting"}
	r := startIntake(c, queue)

	r.events <- ownerEvent("overflow")
	// Intake takes events in order, so this one's ack means the overflow
	// was already dealt with.
	r.events <- strangerEvent("marker")
	if got := <-r.acked; got != "envelope-marker" {
		t.Fatalf("acked %s, want only the marker", got)
	}
	if err := r.finish(t); err != nil {
		t.Fatal(err)
	}
	if len(r.acked) != 0 {
		t.Fatalf("the overflow was acked: %s", <-r.acked)
	}
	if got := inbox.claimed(); len(got) != 0 {
		t.Fatalf("the overflow was claimed: %v", got)
	}
	if len(queue) != 1 || (<-queue).ID != "already-waiting" {
		t.Fatal("the queue changed")
	}
}

func TestAFailedClaimEndsIntakeWithoutAcking(t *testing.T) {
	inbox := &onceInbox{fail: errors.New("disk full")}
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}, inbox: inbox}
	r := startIntake(c, make(chan Message, 4))

	r.events <- ownerEvent("first")
	err := returnsSoon(t, r.done)
	r.stop()
	if err == nil || !strings.Contains(err.Error(), "persist") {
		t.Fatalf("err %v, want the receipt could not be persisted", err)
	}
	if len(r.acked) != 0 {
		t.Fatalf("an event whose claim failed was acked: %s", <-r.acked)
	}
	if len(r.queue) != 0 {
		t.Fatal("an event whose claim failed was queued")
	}
}

// Slack redelivers an event it thinks was missed; the inbox has it already,
// so it is acked again but not answered twice.
func TestARedeliveredEventIsAckedButNotAnsweredAgain(t *testing.T) {
	inbox := &onceInbox{}
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}, inbox: inbox}
	events := make(chan socketmode.Event)
	acked := make(chan string, 4)
	replied := make(chan string, 4)
	var mu sync.Mutex
	handled := 0
	handler := func(_ context.Context, m Message) (string, error) {
		mu.Lock()
		handled++
		mu.Unlock()
		return "answer to " + m.ID, nil
	}
	tr := transport{
		events: events,
		listen: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		ack:    func(_ context.Context, id string) { acked <- id },
		reply:  func(_ context.Context, _ Message, text string) error { replied <- text; return nil },
	}
	graceful, stopTaking := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.run(lifecycle.Stop{Graceful: graceful, Force: context.Background()}, handler, tr) }()

	for range 2 {
		events <- ownerEvent("first")
		if got := <-acked; got != "envelope-first" {
			t.Fatalf("acked %s", got)
		}
	}
	stopTaking()
	if err := returnsSoon(t, done); err != nil {
		t.Fatal(err)
	}
	if got := inbox.claimed(); len(got) != 2 {
		t.Fatalf("claims %v, want the redelivery claimed and found seen", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if handled != 1 || len(replied) != 1 {
		t.Fatalf("handled %d times, replied %d times; want once each", handled, len(replied))
	}
}
