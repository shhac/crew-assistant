package connections

import (
	"context"
	"encoding/json"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"reflect"
	"strings"
	"testing"
)

var linearBindings = []config.Connection{{ID: "lin", Tool: "lin", Profiles: []string{"home"}}}

func TestLinearSessionDiscoversOnceAndKeepsItsAccount(t *testing.T) {
	auth, reads := 0, 0
	c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			auth++
			return []byte(`{"alias":"home"}`), nil
		}
		reads++
		if args[2] == LinearIssueContextQuery {
			return []byte(`{"issue":{"id":"one","description":"Context"}}`), nil
		}
		return []byte(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`), nil
	}}
	ctx := context.Background()
	session, err := c.LinearAccount(ctx, linearBindings, "lin", "home")
	if err != nil {
		t.Fatal(err)
	}
	l := testLinearLink()
	for range 2 {
		if _, err := session.PickUpIssues(ctx, l, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := session.LinearIssueContext(ctx, l, core.LinearIssue{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	l.Profile = "other"
	if _, err := session.PickUpIssues(ctx, l, ""); err == nil {
		t.Fatal("session changed accounts")
	}
	if _, err := session.LinearIssueContext(ctx, l, core.LinearIssue{ID: "one"}); err == nil {
		t.Fatal("context changed accounts")
	}
	if auth != 1 || reads != 3 {
		t.Fatal(auth, reads)
	}
	if _, err := mustLinearSession(t, c).PickUpIssues(ctx, testLinearLink(), ""); err != nil {
		t.Fatal(err)
	}
	if auth != 2 {
		t.Fatal("independent caller bypassed discovery")
	}
}

func testLinearLink() core.LinearLink {
	return core.LinearLink{ConnectionID: "lin", Profile: "home", Kind: "team", ID: "11111111-1111-1111-1111-111111111111", Name: "Team", Rules: core.LinearRules{PickUp: true, States: []string{"Todo"}, Assignee: "any"}}
}
func TestLinearQueryRulesAndFixedArgv(t *testing.T) {
	for _, kind := range []string{"team", "project"} {
		for _, mode := range []string{"any", "unassigned", "me", "users"} {
			t.Run(kind+mode, func(t *testing.T) {
				l := testLinearLink()
				l.Kind = kind
				l.Rules.Assignee = mode
				l.Rules.Users = []core.LinearUser{{ID: l.ID, Name: "User"}}
				c := Client{Run: func(_ context.Context, name string, args []string) ([]byte, error) {
					if name != "lin" {
						t.Fatal(name)
					}
					if args[0] == "auth" {
						return []byte(`{"alias":"home"}`), nil
					}
					if !reflect.DeepEqual(args[:4], []string{"api", "query", LinearPickUpQuery, "--variables"}) || !reflect.DeepEqual(args[5:], []string{"--format", "json", "--workspace", "home"}) {
						t.Fatal(args)
					}
					var vars struct {
						Filter map[string]json.RawMessage `json:"filter"`
						After  string                     `json:"after"`
					}
					if json.Unmarshal([]byte(args[4]), &vars) != nil || vars.After != "cursor" {
						t.Fatal(args[4])
					}
					if _, ok := vars.Filter[kind]; !ok {
						t.Fatal("scope absent")
					}
					if string(vars.Filter["state"]) != `{"name":{"in":["Todo"]}}` {
						t.Fatal(vars.Filter)
					}
					expected := map[string]string{"any": "", "unassigned": `{"null":true}`, "me": `{"isMe":{"eq":true}}`, "users": `{"id":{"in":["` + l.ID + `"]}}`}
					if string(vars.Filter["assignee"]) != expected[mode] {
						t.Fatal(string(vars.Filter["assignee"]))
					}
					return []byte(`{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false}}}`), nil
				}}
				if _, err := mustLinearSession(t, c).PickUpIssues(context.Background(), l, "cursor"); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestLinearInvalidPagesAndScope(t *testing.T) {
	for _, output := range []string{`{}`, `{"errors":[{"message":"failed"}]}`, `{"issues":{"nodes":[]}}`, `{"error":"failed"}`} {
		c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if args[0] == "auth" {
				return []byte(`{"alias":"home"}`), nil
			}
			return []byte(output), nil
		}}
		if _, err := mustLinearSession(t, c).PickUpIssues(context.Background(), testLinearLink(), ""); err == nil {
			t.Fatal("accepted", output)
		}
	}
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) { t.Fatal("out of scope ran"); return nil, nil }}
	l := testLinearLink()
	l.Profile = "outside"
	if _, err := c.LinearAccount(context.Background(), linearBindings, l.ConnectionID, l.Profile); err == nil {
		t.Fatal("outside accepted")
	}
}
func TestLinearOptionCommands(t *testing.T) {
	for kind, expected := range map[string][]string{"teams": {"team", "list", "--limit", "100"}, "projects": {"project", "list", "--limit", "100", "--team", testLinearLink().ID}, "states": {"team", "states", testLinearLink().ID}, "users": {"user", "list", "--limit", "100"}} {
		c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if args[0] == "auth" {
				return []byte(`{"alias":"home"}`), nil
			}
			want := append(expected, "--format", "json", "--workspace", "home")
			if !reflect.DeepEqual(args, want) {
				t.Fatal(args, want)
			}
			return []byte(`[{"id":"one","name":"One"}]`), nil
		}}
		rows, err := c.LinearOptions(context.Background(), linearBindings, "lin", "home", kind, testLinearLink().ID)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
	}
}

func TestLinearOptionPaginationIsCompleteOrFails(t *testing.T) {
	for _, kind := range []string{"teams", "projects", "users"} {
		for _, failure := range []string{"", "read", "cycle", "missing", "cap"} {
			t.Run(kind+failure, func(t *testing.T) {
				calls := 0
				c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
					if args[0] == "auth" {
						return []byte(`{"alias":"home"}`), nil
					}
					calls++
					if calls > 1 && !strings.Contains(strings.Join(args, " "), "--cursor next") {
						t.Fatal(args)
					}
					if failure == "read" && calls == 2 {
						return []byte(`{"error":"unavailable"}`), nil
					}
					more := calls == 1 || failure == "cycle" || failure == "cap" || failure == "missing"
					cursor := "next"
					if failure == "cap" {
						cursor = "next" + strings.Repeat("x", calls-1)
					}
					if failure == "missing" {
						cursor = ""
					}
					data, _ := json.Marshal(map[string]any{"data": []map[string]string{{"id": "one", "name": "One"}}, "@pagination": map[string]any{"has_more": more, "next_cursor": cursor}})
					return data, nil
				}}
				rows, err := c.LinearOptions(context.Background(), linearBindings, "lin", "home", kind, "")
				if failure == "" {
					if err != nil || len(rows) != 2 {
						t.Fatal(rows, err)
					}
				} else if err == nil || rows != nil {
					t.Fatal("partial list accepted", rows, err)
				}
			})
		}
	}
	full, _ := json.Marshal(make([]map[string]string, 100))
	if _, _, err := linearOptionPage(full); err == nil {
		t.Fatal("full page without pagination accepted")
	}
}

func TestLinearContextFailuresAreNotEmptyDescriptions(t *testing.T) {
	for _, output := range []string{`{}`, `{"error":"failed"}`, `{"issue":{"id":"wrong"}}`} {
		c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if args[0] == "auth" {
				return []byte(`{"alias":"home"}`), nil
			}
			return []byte(output), nil
		}}
		if _, err := mustLinearSession(t, c).LinearIssueContext(context.Background(), testLinearLink(), core.LinearIssue{ID: "one"}); err == nil {
			t.Fatal(output)
		}
	}
	var b bounded
	if _, err := b.Write(make([]byte, 129*1024)); err != ErrResponseTooLarge || !b.exceeded {
		t.Fatal("runner lost overflow", err)
	}
}

func TestLinearProjectTeamsIsScopedAndComplete(t *testing.T) {
	for _, output := range []string{
		`{"project":{"teams":{"nodes":[{"id":"one","name":"Engineering"}],"pageInfo":{"hasNextPage":false}}}}`,
		`{"project":{"teams":{"pageInfo":{"hasNextPage":true}}}}`,
		`{"project":{}}`, `{"errors":[{"message":"failed"}]}`,
	} {
		c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if args[0] == "auth" {
				return []byte(`{"alias":"home"}`), nil
			}
			want := []string{"api", "query", LinearProjectTeamsQuery, "--variables", `{"id":"` + testLinearLink().ID + `"}`, "--format", "json", "--workspace", "home"}
			if !reflect.DeepEqual(args, want) {
				t.Fatal(args, want)
			}
			return []byte(output), nil
		}}
		rows, err := c.LinearOptions(context.Background(), linearBindings, "lin", "home", "project-teams", testLinearLink().ID)
		valid := output == `{"project":{"teams":{"nodes":[{"id":"one","name":"Engineering"}],"pageInfo":{"hasNextPage":false}}}}`
		if valid && (err != nil || len(rows) != 1) || !valid && err == nil {
			t.Fatal(rows, err)
		}
	}
}

func TestLinearDescriptionIsDecodedAndInvalidBoundsAreRefused(t *testing.T) {
	c := Client{Run: func(_ context.Context, _ string, args []string) ([]byte, error) {
		if args[0] == "auth" {
			return []byte(`{"alias":"home"}`), nil
		}
		if args[2] != LinearIssueContextQuery || args[4] != `{"id":"one"}` {
			t.Fatal(args)
		}
		return []byte(`{"issue":{"id":"one","description":"Keep column names."}}`), nil
	}}
	description, err := mustLinearSession(t, c).LinearIssueContext(context.Background(), testLinearLink(), core.LinearIssue{ID: "one"})
	if err != nil || description != "Keep column names." {
		t.Fatal(description, err)
	}
	refuse := &LinearSession{id: "lin", profile: "home", client: Client{Run: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("invalid bounds ran")
		return nil, nil
	}}}
	for _, mutate := range []func(*core.LinearLink){func(l *core.LinearLink) { l.ID = "not-an-id" }, func(l *core.LinearLink) { l.Rules.States = []string{strings.Repeat("x", 257)} }, func(l *core.LinearLink) {
		l.Rules.Assignee = "users"
		l.Rules.Users = []core.LinearUser{{ID: "bad", Name: "User"}}
	}} {
		l := testLinearLink()
		mutate(&l)
		if _, err := refuse.PickUpIssues(context.Background(), l, ""); err == nil {
			t.Fatal("invalid accepted", l)
		}
	}
	if _, err := refuse.PickUpIssues(context.Background(), testLinearLink(), strings.Repeat("x", 257)); err == nil {
		t.Fatal("long cursor accepted")
	}
}

func mustLinearSession(t *testing.T, c Client) *LinearSession {
	t.Helper()
	session, err := c.LinearAccount(context.Background(), linearBindings, "lin", "home")
	if err != nil {
		t.Fatal(err)
	}
	return session
}
