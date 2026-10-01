package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"testing"
)

func event() slackevents.EventsAPIEvent {
	return slackevents.EventsAPIEvent{TeamID: "workspace-one", Data: &slackevents.EventsAPICallbackEvent{EventID: "event-one"}, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{User: "owner-one", ChannelType: "im", Channel: "dm-one", Text: "What needs a decision?", TimeStamp: "100.1"}}}
}
func TestOwnerDMIsAccepted(t *testing.T) {
	msg, ok := ParseOwnerMessage("owner-one", event())
	if !ok || msg.ID != "event-one" || msg.ThreadTS != "100.1" {
		t.Fatalf("owner DM lost: %#v", msg)
	}
}
func TestOwnerGateRejectsOtherSources(t *testing.T) {
	mutations := []func(*slackevents.EventsAPIEvent){func(e *slackevents.EventsAPIEvent) { e.InnerEvent.Data.(*slackevents.MessageEvent).User = "other-user" }, func(e *slackevents.EventsAPIEvent) {
		e.InnerEvent.Data.(*slackevents.MessageEvent).ChannelType = "channel"
	}, func(e *slackevents.EventsAPIEvent) { e.InnerEvent.Data.(*slackevents.MessageEvent).BotID = "bot" }, func(e *slackevents.EventsAPIEvent) {
		e.InnerEvent.Data.(*slackevents.MessageEvent).SubType = "message_changed"
	}, func(e *slackevents.EventsAPIEvent) { e.IsExtSharedChannel = true }, func(e *slackevents.EventsAPIEvent) { e.Data = nil }, func(e *slackevents.EventsAPIEvent) { e.InnerEvent.Data.(*slackevents.MessageEvent).Text = " " }}
	for i, mutate := range mutations {
		e := event()
		mutate(&e)
		if _, ok := ParseOwnerMessage("owner-one", e); ok {
			t.Errorf("untrusted event %d accepted", i)
		}
	}
}
func TestThreadRepliesStayInThread(t *testing.T) {
	e := event()
	e.InnerEvent.Data.(*slackevents.MessageEvent).ThreadTimeStamp = "99.1"
	m, ok := ParseOwnerMessage("owner-one", e)
	if !ok || m.ThreadTS != "99.1" {
		t.Fatal("thread lost")
	}
}

func TestOwnerMessageIsAcceptedOnlyInTheConfiguredWorkspace(t *testing.T) {
	c := &Client{cfg: Config{OwnerUserID: "owner-one", WorkspaceID: "workspace-one"}}
	for _, team := range []string{"", "workspace-other", "workspace-one"} {
		e := event()
		e.TeamID = team
		_, ok := c.ownerMessage(socketmode.Event{Type: socketmode.EventTypeEventsAPI, Data: e})
		if ok != (team == "workspace-one") {
			t.Errorf("workspace %q accepted: %v", team, ok)
		}
	}
	c.cfg.WorkspaceID = ""
	if _, ok := c.ownerMessage(ownerEvent("one")); ok {
		t.Fatal("unconfigured workspace accepted a message")
	}
}

func TestBotWorkspaceIsVerifiedBeforeAnyOutboundMessage(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantError  bool
	}{
		{"matching", `{"ok":true,"team_id":"workspace-one"}`, false},
		{"other workspace", `{"ok":true,"team_id":"workspace-other"}`, true},
		{"missing workspace", `{"ok":true}`, true},
		{"provider error", `{"ok":false,"error":"private-fixture-secret"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth.test" {
					t.Errorf("unexpected Slack call: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			c := &Client{cfg: Config{WorkspaceID: "workspace-one"}, api: slackapi.New("synthetic-token", slackapi.OptionAPIURL(server.URL+"/"))}
			err := c.VerifyWorkspace(context.Background())
			if (err != nil) != test.wantError {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "private-fixture-secret") {
				t.Fatal("provider detail escaped into an error")
			}
		})
	}
}
