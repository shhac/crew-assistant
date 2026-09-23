package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/testutil"
)

func TestAssignedUsesViewerScopeAndPagination(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "linear-fixture-secret")
	calls := 0
	s := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "linear-fixture-secret" {
			t.Error("missing credential")
		}
		var req struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "AssistantViewer") {
			_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"owner-1"}}}`))
			return
		}
		if req.Variables["owner"] != "owner-1" {
			t.Error("did not scope to viewer")
		}
		teams := req.Variables["teams"].([]any)
		if len(teams) != 1 || teams[0] != "team-1" {
			t.Error("incorrect team scope")
		}
		if req.Variables["after"] == nil {
			_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":"issue-1","title":"Outcome","team":{"id":"team-1"}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}`))
			return
		}
		if req.Variables["after"] != "cursor-1" {
			t.Error("incorrect cursor")
		}
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[{"id":"issue-2","title":"Another outcome","team":{"id":"team-1"}}],"pageInfo":{"hasNextPage":false}}}}`))
	}))
	defer s.Close()
	c, err := New(Config{Endpoint: s.URL, APIKeyEnv: "TEST_LINEAR_KEY", TeamIDs: []string{"team-1"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Assigned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) != 2 || calls != 3 || result.OwnerID != "owner-1" {
		t.Fatalf("bad assignment result %#v", result)
	}
}
func TestAssignedRejectsPartialGraphQLErrorAndScopeLeak(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "private-key")
	for _, body := range []string{`{"data":{"viewer":{"id":"owner"}},"errors":[{"message":"private-key"}]}`, `{"data":{"viewer":{"id":"owner"},"issues":{"nodes":[{"id":"issue","team":{"id":"other-team"}}],"pageInfo":{"hasNextPage":false}}}}`} {
		s := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		c, _ := New(Config{Endpoint: s.URL, APIKeyEnv: "TEST_LINEAR_KEY", TeamIDs: []string{"team"}})
		_, err := c.Assigned(context.Background())
		s.Close()
		if err == nil || strings.Contains(err.Error(), "private-key") {
			t.Fatalf("invalid response accepted or leaked: %v", err)
		}
	}
}
func TestAssignedTimeout(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "fixture")
	s := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(50 * time.Millisecond):
		}
	}))
	defer s.Close()
	c, _ := New(Config{Endpoint: s.URL, APIKeyEnv: "TEST_LINEAR_KEY", TeamIDs: []string{"team"}, Timeout: 10 * time.Millisecond})
	if _, err := c.Assigned(context.Background()); err == nil {
		t.Fatal("timeout ignored")
	}
}
func TestRequiresExplicitTeamScope(t *testing.T) {
	if _, err := New(Config{APIKeyEnv: "TOKEN"}); err == nil {
		t.Fatal("unscoped Linear discovery permitted")
	}
}
func TestRepeatedCursorFails(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "fixture")
	s := testutil.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"owner"},"issues":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"same"}}}}`))
	}))
	defer s.Close()
	c, _ := New(Config{Endpoint: s.URL, APIKeyEnv: "TEST_LINEAR_KEY", TeamIDs: []string{"team"}})
	if _, err := c.Assigned(context.Background()); err == nil {
		t.Fatal("repeated cursor permitted")
	}
}
