package connections

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shhac/crew-assistant/internal/config"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoveryOnlyReturnsAccountAliases(t *testing.T) {
	for _, tool := range []string{"lin", "agent-slack", "agent-notion", "agent-fathom"} {
		c := Client{Run: func(_ context.Context, name string, args []string) ([]byte, error) {
			if name != tool || args[0] != "auth" {
				t.Fatal(name, args)
			}
			return []byte(`{"alias":"work","token":"not-for-output","name":"Private org"}` + "\n" + `{"profile":"personal","secret":"not-for-output"}`), nil
		}}
		d, err := c.Discover(context.Background(), tool)
		if err != nil || !d.Available || len(d.Profiles) != 2 || d.Profiles[0].Name != "personal" {
			t.Fatal(d, err)
		}
		body, _ := json.Marshal(d)
		if strings.Contains(string(body), "not-for-output") || strings.Contains(string(body), "Private org") {
			t.Fatal(string(body))
		}
		if d.Selectable != (tool != "agent-notion") {
			t.Fatal(d)
		}
	}
}
func TestExactProfileScopeAndArgumentInjection(t *testing.T) {
	binding := []config.Connection{{ID: "slack", Tool: "agent-slack", Profiles: []string{"work"}}}
	var invoked []string
	c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"work"}`), nil
		}
		invoked = args
		return []byte(`{"text":"result"}`), nil
	}}
	q := Query{ConnectionID: "slack", Profile: "work", Operation: "search", Query: "--download $(touch /tmp/should-not-exist)"}
	result, err := c.Query(context.Background(), binding, q)
	if err != nil || len(result.Data) != 1 {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(invoked[len(invoked)-2:], []string{"--", q.Query}) {
		t.Fatal(invoked)
	}
	invoked = nil
	q.Profile = "another"
	if _, err = c.Query(context.Background(), binding, q); err == nil || invoked != nil {
		t.Fatal("out of scope profile reached CLI")
	}
	q.Profile = "wor"
	binding[0].Profiles = []string{"wor"}
	if _, err = c.Query(context.Background(), binding, q); err == nil || invoked != nil {
		t.Fatal("substring selector accepted")
	}
}
func TestRejectWritesUnknownOperationsAndNotionBeforeSpawn(t *testing.T) {
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("must reject before subprocess")
		return nil, nil
	}}
	for _, tool := range []string{"lin", "agent-slack", "agent-notion", "agent-fathom"} {
		for _, op := range []string{"send", "api", "purchase", "exec"} {
			_, err := c.Query(context.Background(), []config.Connection{{ID: "x", Tool: tool, Profiles: []string{"work"}}}, Query{ConnectionID: "x", Profile: "work", Operation: op})
			if err == nil {
				t.Fatal(tool, op)
			}
		}
	}
}
func TestReadErrorsDoNotExposeSubprocessDiagnostics(t *testing.T) {
	c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"profile":"work"}`), nil
		}
		return nil, errors.New("secret diagnostic")
	}}
	_, err := c.Query(context.Background(), []config.Connection{{ID: "f", Tool: "agent-fathom", Profiles: []string{"work"}}}, Query{ConnectionID: "f", Profile: "work", Operation: "meetings"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
func TestBoundsAndMalformedOutput(t *testing.T) {
	var b bounded
	if _, err := b.Write(make([]byte, 128*1024+1)); err == nil {
		t.Fatal("unbounded")
	}
	if _, err := decode([]byte("not json")); err == nil {
		t.Fatal("invalid JSON")
	}
	got, err := decode([]byte(`{"data":[{"id":"x"}]}`))
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
}
func TestAllOperationsAreFixedReadCommands(t *testing.T) {
	for _, tool := range []string{"lin", "agent-slack", "agent-fathom"} {
		for _, op := range Operations(tool) {
			id := map[string]string{"lin": "EX-1", "agent-slack": "C0123456789", "agent-fathom": "123"}[tool]
			a, err := arguments(tool, Query{Profile: "work", Operation: op, Query: "example", ResourceID: id})
			if err != nil || len(a) == 0 {
				t.Fatal(tool, op, a, err)
			}
		}
	}
}

func TestResourceTargetsCannotSwitchAccountsOrCreateDMs(t *testing.T) {
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid resource reached a subprocess")
		return nil, nil
	}}
	cases := []struct{ tool, operation, id string }{
		{"agent-slack", "messages", "https://other.slack.com/archives/C0123456789/p1234567890123456"},
		{"agent-slack", "messages", "slack://channel?id=C0123456789&team=T12345678"},
		{"agent-slack", "messages", "U0123456789"},
		{"agent-slack", "messages", "@colleague"},
		{"agent-slack", "messages", "#general"},
		{"agent-slack", "messages", "C0123456789 --workspace other"},
		{"lin", "issue", "https://linear.app/other/issue/EX-1"},
		{"agent-fathom", "summary", "123/../../meetings"},
	}
	for _, tc := range cases {
		t.Run(tc.tool+tc.id, func(t *testing.T) {
			_, err := c.Query(context.Background(), []config.Connection{{ID: "chosen", Tool: tc.tool, Profiles: []string{"work"}}}, Query{ConnectionID: "chosen", Profile: "work", Operation: tc.operation, ResourceID: tc.id})
			if err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
	for _, id := range []string{"C0123456789", "G0123456789", "D0123456789"} {
		if _, err := arguments("agent-slack", Query{Profile: "work", Operation: "messages", ResourceID: id}); err != nil {
			t.Fatal(id, err)
		}
	}
}

func TestSlackWorkspaceEnvelopeDiscoveryAndQuery(t *testing.T) {
	calls := 0
	c := Client{Run: func(_ context.Context, name string, args []string) ([]byte, error) {
		if name != "agent-slack" {
			t.Fatal(name)
		}
		if args[0] == "auth" {
			return []byte(`{"credentials_path":"private-path","default_workspace":"work","workspaces":[{"alias":"work","secrets":{"xoxc":"missing","xoxd":"missing"},"hint":"untrusted secret detail"},{"alias":"personal","secrets":{"token":"keychain"}}]}`), nil
		}
		calls++
		if !strings.Contains(strings.Join(args, " "), "--workspace work search messages") {
			t.Fatal(args)
		}
		return []byte(`{"content":"synthetic reply"}`), nil
	}}
	d, err := c.Discover(context.Background(), "agent-slack")
	if err != nil || len(d.Profiles) != 2 {
		t.Fatal(d, err)
	}
	if d.Profiles[1].Name != "work" || !strings.Contains(d.Profiles[1].Detail, "credential unavailable") {
		t.Fatal(d)
	}
	raw, _ := json.Marshal(d)
	for _, forbidden := range []string{"private-path", "untrusted secret detail", "xoxc", "xoxd"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal(string(raw))
		}
	}
	result, err := c.Query(context.Background(), []config.Connection{{ID: "slack", Tool: "agent-slack", Profiles: []string{"work"}}}, Query{ConnectionID: "slack", Profile: "work", Operation: "search", Query: "project"})
	if err != nil || len(result.Data) != 1 || calls != 1 {
		t.Fatal(result, calls, err)
	}
}
func TestNotionDefaultAccountReadsWithoutAccountDiscoveryOrSelector(t *testing.T) {
	var invoked []string
	c := Client{Run: func(_ context.Context, name string, args []string) ([]byte, error) {
		if name != "agent-notion" || args[0] == "auth" {
			t.Fatal("Notion default must not depend on stored profile enumeration", name, args)
		}
		invoked = args
		return []byte(`{"id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}`), nil
	}}
	bindings := []config.Connection{{ID: "notion", Tool: "agent-notion", Profiles: []string{}}}
	for _, op := range Operations("agent-notion") {
		result, err := c.Query(context.Background(), bindings, Query{ConnectionID: "notion", Operation: op, Query: "--backend malicious", ResourceID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
		if err != nil || len(result.Data) != 1 || result.Profile != "" {
			t.Fatal(result, err)
		}
		for _, arg := range invoked {
			if arg == "--workspace" || arg == "--profile" || arg == "switch" {
				t.Fatal("invented profile selection", invoked)
			}
		}
		if op == "search" && !reflect.DeepEqual(invoked[len(invoked)-2:], []string{"--", "--backend malicious"}) {
			t.Fatal(invoked)
		}
	}
	invoked = nil
	if _, err := c.Query(context.Background(), bindings, Query{ConnectionID: "notion", Profile: "work", Operation: "search", Query: "project"}); err == nil || invoked != nil {
		t.Fatal("named profile silently ignored")
	}
	bindings[0].Profiles = []string{"work"}
	if _, err := c.Query(context.Background(), bindings, Query{ConnectionID: "notion", Operation: "search", Query: "project"}); err == nil || invoked != nil {
		t.Fatal("saved named profile silently ignored")
	}
}
func TestNotionInvalidResourcesAndWritesNeverSpawn(t *testing.T) {
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid operation reached subprocess")
		return nil, nil
	}}
	binding := []config.Connection{{ID: "notion", Tool: "agent-notion"}}
	for _, q := range []Query{
		{Operation: "page", ResourceID: "https://notion.so/another-workspace"},
		{Operation: "blocks", ResourceID: "--content evil"},
		{Operation: "append", ResourceID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{Operation: "ai", Query: "edit everything"},
	} {
		q.ConnectionID = "notion"
		if _, err := c.Query(context.Background(), binding, q); err == nil {
			t.Fatal(q)
		}
	}
}

func TestOnlyNotionDefaultReceivesItsNativeEnvironmentCredentials(t *testing.T) {
	t.Setenv("NOTION_API_KEY", "synthetic-notion-key")
	t.Setenv("NOTION_TOKEN", "synthetic-notion-token")
	t.Setenv("OPENAI_API_KEY", "synthetic-provider-key")
	t.Setenv("SLACK_TOKEN", "synthetic-other-account-token")
	for _, tool := range []string{"agent-notion", "lin", "agent-slack", "agent-fathom"} {
		values := map[string]string{}
		for _, e := range commandEnvironment(tool, []string{"--format", "jsonl"}) {
			key, value, _ := strings.Cut(e, "=")
			values[key] = value
		}
		if _, ok := values["OPENAI_API_KEY"]; ok {
			t.Fatal("provider credential forwarded")
		}
		if _, ok := values["SLACK_TOKEN"]; ok {
			t.Fatal("named Slack account overridden")
		}
		if tool == "agent-notion" {
			if values["NOTION_API_KEY"] != "synthetic-notion-key" || values["NOTION_TOKEN"] != "synthetic-notion-token" {
				t.Fatal("native default auth dropped")
			}
		} else {
			if _, ok := values["NOTION_API_KEY"]; ok {
				t.Fatal("Notion credential sent to another CLI")
			}
		}
	}
}
