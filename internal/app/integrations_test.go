package app

import (
	"reflect"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestConfiguredIntegrationsDescribeEachConnection(t *testing.T) {
	cfg := config.Default()
	seated(&cfg).Name = "Quill"
	seated(&cfg).Model = config.Model{Engine: "openai-compatible", Model: "gpt-x", Effort: "low"}
	cfg.Connections = []config.Connection{
		{ID: "work", Name: "Work", Tool: "lin", Profiles: []string{"a", "b"}},
		{ID: "mine", Name: "Mine", Tool: "lin", Profiles: []string{"me"}, ImportAssignments: true},
		{ID: "notes", Name: "Notes", Tool: "agent-notion"},
		{ID: "wiki", Name: "Wiki", Tool: "agent-notion", Profiles: []string{"x"}},
	}
	list, ignoreLive := configuredIntegrations(cfg)
	want := []core.Integration{
		{ID: "model", Name: "Assistant model", Status: "configured", Detail: "openai-compatible / gpt-x / low"},
		{ID: "slack", Name: "Slack bot messaging", Status: "not_configured", Detail: "Sends and receives owner direct messages. Configure owner identity and Socket Mode credentials"},
		{ID: "connection:work", Name: "Work", Status: "configured", Detail: "Reading only, through CLI accounts: a, b; optional resource, assignment import off"},
		{ID: "connection:mine", Name: "Mine", Status: "configured", Detail: "Reading only, through CLI accounts: me"},
		{ID: "connection:notes", Name: "Notes", Status: "configured", Detail: "Reading only, through the CLI default account"},
		{ID: "connection:wiki", Name: "Wiki", Status: "unavailable", Detail: "Choose the CLI default account for Notion"},
	}
	if !reflect.DeepEqual(list, want) {
		t.Fatalf("%+v", list)
	}
	if !reflect.DeepEqual(ignoreLive, map[string]bool{"connection:work": true}) {
		t.Fatal(ignoreLive)
	}
}

func TestConfiguredIntegrationsAskForTheSeatedModel(t *testing.T) {
	cfg := config.Default()
	seated(&cfg).Name = "Quill"
	seated(&cfg).Model.Model = ""
	cfg.Linear.ImportAssignments = true
	cfg.Linear.TeamIDs = []string{"team"}
	list, _ := configuredIntegrations(cfg)
	if len(list) != 3 || list[0].Status != "not_configured" || list[0].Detail != "Choose Quill's model on the Team page" || list[2].ID != "linear" {
		t.Fatalf("%+v", list)
	}
	cfg.Assistant.Seat = ""
	if list, _ := configuredIntegrations(cfg); list[0].Detail != "Choose your assistant in Settings" {
		t.Fatal(list[0])
	}
}

func TestWithLiveStatusesKeepsConfiguredLinksAndIgnoresStaleImports(t *testing.T) {
	list := []core.Integration{
		{ID: "slack", Status: "not_configured", ProjectID: "p1"},
		{ID: "connection:work", Status: "configured"},
	}
	live := map[string]core.Integration{
		"slack":           {ID: "slack", Status: "connected", ProjectID: "other"},
		"connection:work": {ID: "connection:work", Status: "error"},
		"chat":            {ID: "chat", Status: "busy"},
	}
	got := withLiveStatuses(list, live, map[string]bool{"connection:work": true})
	want := []core.Integration{
		{ID: "slack", Status: "connected", ProjectID: "p1"},
		{ID: "connection:work", Status: "configured"},
		{ID: "chat", Status: "busy"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}
