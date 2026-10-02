package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/catalog"
)

func TestMemberSaveRefusesOnlyKnownToollessModelsBeforeUpdating(t *testing.T) {
	for _, tc := range []struct {
		name    string
		models  []catalog.Model
		problem error
		refused bool
	}{
		{"no tools", []catalog.Model{{ID: "chosen", ParametersKnown: true, Parameters: []string{"temperature"}}}, nil, true},
		{"tools", []catalog.Model{{ID: "chosen", ParametersKnown: true, Parameters: []string{"tools"}}}, nil, false},
		{"unknown parameters", []catalog.Model{{ID: "chosen"}}, nil, false},
		{"unlisted", []catalog.Model{{ID: "other", ParametersKnown: true}}, nil, false},
		{"discovery failure", nil, errors.New("synthetic provider failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Engines.Providers = []config.Provider{{ID: "fixture-api", Name: "Fixture API", HTTPEngine: config.HTTPEngine{BaseURL: "http://127.0.0.1:1234/v1"}}}
			a, _, _ := newDashboard(t, cfg)
			m, err := a.Core.SaveMember(context.Background(), "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "claude"})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			lookup := newModelLookup(func(_ context.Context, p harness.Provider) ([]catalog.Model, error) {
				calls++
				if p.API.BaseURL != cfg.Engines.Providers[0].BaseURL {
					t.Fatal("wrong provider", p.API.BaseURL)
				}
				return tc.models, tc.problem
			})
			mux := http.NewServeMux()
			registerMembers(mux, a, lookup)
			mux.Handle("GET /api/models", modelHandlerWithLookup(a, lookup))
			// Fill the same catalog cache the save uses, without a real provider.
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/models?engine=openai-compatible&provider=fixture-api", nil))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/members/"+m.ID, strings.NewReader(`{"name":"Rune","kinds":["reviewer"],"engine":"openai-compatible","provider":"fixture-api","model":" chosen "}`)))
			if tc.refused {
				if w.Code != 400 || !strings.Contains(w.Body.String(), "This model can't call tools") {
					t.Fatal(w.Code, w.Body.String())
				}
			} else if config.Supports("openai-compatible", config.UseRoles) {
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"provider":"fixture-api"`) {
					t.Fatal(w.Code, w.Body.String())
				}
			} else if w.Code != 400 || !strings.Contains(w.Body.String(), "can't run team roles on this computer") {
				t.Fatal(w.Code, w.Body.String())
			}
			v, err := a.Core.Snapshot(context.Background())
			if err != nil || len(v.Members) != 1 || calls != 1 {
				t.Fatal(v.Members, err, calls)
			}
			if tc.refused && v.Members[0].Engine != "claude" {
				t.Fatal("refusal changed the stored member")
			}
			// Creation applies the same check before adding a member or drawing.
			if tc.refused {
				w = httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/members", strings.NewReader(`{"name":"Moss","kinds":["reviewer"],"engine":"openai-compatible","provider":"fixture-api","model":" chosen "}`)))
				if w.Code != 400 {
					t.Fatal("creation bypassed model check", w.Code)
				}
				v, _ = a.Core.Snapshot(context.Background())
				if len(v.Members) != 1 {
					t.Fatal("refused creation wrote state")
				}
			}
		})
	}
}

func TestUnknownMemberProviderUsesOwnerTextForCreateAndUpdate(t *testing.T) {
	a, _, _ := newDashboard(t, config.Default())
	mux := http.NewServeMux()
	registerMembers(mux, a)
	for _, method := range []string{"POST", "PUT"} {
		path := "/api/members"
		if method == "PUT" {
			path += "/fixture"
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(`{"name":"Rune","kinds":["reviewer"],"engine":"openai-compatible","provider":"missing","model":"tools"}`)))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "Provider missing") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestEmptyAPIMemberModelIsRefusedBeforeDiscovery(t *testing.T) {
	a, _, _ := newDashboard(t, config.Default())
	member, err := a.Core.SaveMember(context.Background(), "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	lookup := newModelLookup(func(context.Context, harness.Provider) ([]catalog.Model, error) {
		t.Fatal("empty model triggered discovery")
		return nil, nil
	})
	mux := http.NewServeMux()
	registerMembers(mux, a, lookup)
	for _, method := range []string{"POST", "PUT"} {
		path := "/api/members"
		if method == "PUT" {
			path += "/" + member.ID
		}
		for _, model := range []string{"", "   "} {
			w := httptest.NewRecorder()
			body := `{"name":"Ash","kinds":["reviewer"],"engine":"openai-compatible","model":"` + model + `"}`
			mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
			if w.Code != 400 || !strings.Contains(w.Body.String(), "Choose a model for this member: another API has no default") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	}
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil || len(snap.Members) != 1 || snap.Members[0].Name != "Rune" {
		t.Fatal(snap.Members, err)
	}
}
