package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/testutil"
)

func setupModel(t *testing.T, a *App, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := testutil.NewServer(t, handler)
	t.Cleanup(server.Close)
	cfg := a.Config()
	seated(&cfg).Model.Engine = "openai-compatible"
	seated(&cfg).Model.Model = "fixture-model"
	seated(&cfg).Model.Effort = ""
	cfg.Engines.OpenAICompatible.APIKeyEnv = ""
	cfg.Engines.OpenAICompatible.BaseURL = server.URL + "/v1"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return server
}
func writeSetupCall(w http.ResponseWriter, name string, arguments any) {
	encoded, _ := json.Marshal(arguments)
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "fixture-call", "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}}}})
}
func fixtureDrawing() map[string]any {
	return map[string]any{"background": "#10182a", "marks": []any{
		map[string]any{"d": "M28 92C18 39 65 21 106 23 108 77 76 108 28 92Z", "color": "#91b5e8", "stroke_width": 0},
		map[string]any{"d": "M31 91\n83 43", "color": "#10182a", "stroke_width": 6},
	}}
}
func fixtureProposal() map[string]any {
	return map[string]any{"name": "Juniper", "personality": "Be concise and calm; bring a recommendation with the evidence.", "avatar": fixtureDrawing(), "look": "Short silver hair and a green scarf", "rationale": "A calm botanical identity suits the preference for measured communication."}
}

type setupRequest struct {
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Messages []struct{ Role, Content string } `json:"messages"`
}

func TestASuggestionForANewAssistantChangesNothing(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		var request setupRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 2 {
			t.Errorf("setup exposed %d tools", len(request.Tools))
		}
		for _, tool := range request.Tools {
			if tool.Function.Name != "ask_setup_questions" && tool.Function.Name != "propose_identity" {
				t.Error("project tool exposed to setup", tool.Function.Name)
			}
		}
		if !strings.Contains(request.Messages[0].Content, "a new assistant of theirs") || !strings.Contains(request.Messages[1].Content, `"Quill"`) {
			t.Errorf("the prompt should say who is suggested and which names are taken: %+v", request.Messages[:2])
		}
		if calls.Add(1) == 1 {
			writeSetupCall(w, "ask_setup_questions", map[string]any{"questions": []string{"Should their tone be calm or energetic?", "Do you prefer a human or nature-inspired name?"}})
			return
		}
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "calm") {
			t.Error("owner preferences missing")
		}
		writeSetupCall(w, "propose_identity", fixtureProposal())
	})
	original := a.Config()
	state, err := a.InterviewIdentity(ctx, SetupAssistant, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Questions) != 2 || len(state.Messages) != 2 || state.Recommendation != nil {
		t.Fatalf("missing interview: %+v", state)
	}
	state, err = a.InterviewIdentity(ctx, SetupAssistant, "Keep it calm, with a nature-inspired name.")
	if err != nil {
		t.Fatal(err)
	}
	rec := state.Recommendation
	if rec == nil || rec.ID == "" || rec.Name != "Juniper" || rec.Avatar.Look != "Short silver hair and a green scarf" || rec.Avatar.Marks[1].D != "M31 91 83 43" || !strings.Contains(rec.AvatarSVG, "<svg") {
		t.Fatalf("missing preview: %+v", state)
	}
	if !reflect.DeepEqual(a.Config(), original) {
		t.Fatal("a suggestion changed the config")
	}
	restored, err := a.IdentitySetup(SetupAssistant)
	if err != nil || restored.Recommendation.ID != rec.ID {
		t.Fatal("setup did not persist", err)
	}
	info, err := os.Stat(a.configPath + ".setup-assistant.json")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("setup is not private", err)
	}
	if other, err := a.IdentitySetup(SetupMember); err != nil || len(other.Messages) != 0 {
		t.Fatalf("a member's suggestion shares the assistant's: %+v %v", other, err)
	}
	if err := a.ResetIdentitySetup(SetupAssistant); err != nil {
		t.Fatal(err)
	}
	if fresh, err := a.IdentitySetup(SetupAssistant); err != nil || len(fresh.Messages) != 0 || fresh.Recommendation != nil {
		t.Fatalf("starting over kept %+v %v", fresh, err)
	}
}

// A member's suggestion is for someone on project teams, and never takes a
// name another member has or one that names a role.
func TestASuggestionForANewMember(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Juniper", Kinds: []string{core.RoleImplementer}, Engine: "claude"}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		var request setupRequest
		json.NewDecoder(r.Body).Decode(&request)
		if !strings.Contains(request.Messages[0].Content, "a new member of their team") || !strings.Contains(request.Messages[1].Content, `"Juniper"`) || strings.Contains(request.Messages[1].Content, `"Quill"`) {
			t.Errorf("the prompt should be about a member and the members' names: %+v", request.Messages[:2])
		}
		proposal := fixtureProposal()
		switch calls.Add(1) {
		case 1:
			// Taken by a member already, so it is sent back once.
		case 2:
			proposal["name"] = "Moss"
		case 3:
			proposal["name"] = "Reviewer"
		case 4:
			proposal["name"] = strings.Repeat("M", 41)
		}
		writeSetupCall(w, "propose_identity", proposal)
	})
	state, err := a.InterviewIdentity(ctx, SetupMember, "Someone careful")
	if err != nil || state.Recommendation == nil || state.Recommendation.Name != "Moss" || calls.Load() != 2 {
		t.Fatalf("%+v %v after %d calls", state.Recommendation, err, calls.Load())
	}
	if _, err := a.InterviewIdentity(ctx, SetupMember, "Another"); !errors.Is(err, errUnusableProposal) {
		t.Fatalf("a member named for a role or too long was suggested: %v", err)
	}
	if _, err := a.InterviewIdentity(ctx, "project", "Anyone"); !errors.Is(err, ErrSetupSubject) {
		t.Fatalf("a suggestion for something else: %v", err)
	}
}

// With no one in the seat the first assistant makes suggestions; with no
// assistants at all, the model chosen for small jobs does, so the first one
// can still be suggested.
func TestSuggestionsWithoutASeatedAssistant(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeSetupCall(w, "ask_setup_questions", map[string]any{"questions": []string{"What tone?"}})
	})
	cfg := a.Config()
	cfg.Assistant.Seat = ""
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InterviewIdentity(context.Background(), SetupAssistant, ""); err != nil || calls.Load() != 1 {
		t.Fatal(err, calls.Load())
	}
	if err := a.DeleteAssistant(context.Background(), "milo"); err != nil {
		t.Fatal(err)
	}
	cfg = a.Config()
	cfg.Models.Suggestions = config.SmallModel{Engine: "openai-compatible", Model: "fixture-small"}
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state, err := a.InterviewIdentity(context.Background(), SetupAssistant, "")
	if err != nil || calls.Load() != 2 || len(state.Questions) != 1 {
		t.Fatal(state, err, calls.Load())
	}
}

// With no assistants and no model chosen for small jobs, the default
// assistant's model makes the suggestion.
func TestSuggestionsWithNoAssistantsFallBackToTheDefaultModel(t *testing.T) {
	a := testApp(t)
	if err := a.DeleteAssistant(context.Background(), "milo"); err != nil {
		t.Fatal(err)
	}
	ec := a.setupModel(context.Background(), a.Config())
	want := config.DefaultProfile().Model
	if ec.Engine != want.Engine || ec.Model != want.Model || ec.Effort != want.Effort {
		t.Fatalf("%+v", ec)
	}
}

func TestSetupRejectsProjectToolsAndUnsafeAvatar(t *testing.T) {
	for _, test := range []struct {
		name      string
		tool      string
		arguments any
	}{
		{"project action", "create_project", map[string]any{"title": "Unwanted", "objective": "Never"}},
		{"markup avatar", "propose_identity", map[string]any{"name": "Juniper", "personality": "Calm", "rationale": "A useful recommendation", "avatar": map[string]any{"background": "#10182a", "marks": []any{map[string]any{"d": "M0 0\"/><script>alert(1)</script>", "color": "#ffffff", "stroke_width": 0}}}}},
		{"preset avatar", "propose_identity", map[string]any{"name": "Juniper", "personality": "Calm", "rationale": "A useful recommendation", "avatar": map[string]any{"shape": "orb", "background": "#10182a", "accent": "#ffffff"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApp(t)
			setupModel(t, a, func(w http.ResponseWriter, r *http.Request) { writeSetupCall(w, test.tool, test.arguments) })
			original := a.Config()
			if _, err := a.InterviewIdentity(context.Background(), SetupAssistant, "Suggest an identity"); err == nil {
				t.Fatal("unsafe setup result accepted")
			}
			state, err := a.IdentitySetup(SetupAssistant)
			if err != nil || state.Recommendation != nil {
				t.Fatal("unsafe proposal persisted", err)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			if len(snap.Projects) != 0 || !reflect.DeepEqual(a.Config(), original) {
				t.Fatal("setup escaped identity scope")
			}
		})
	}
}

// A drawing that is not usable is sent back once with the reason, so the
// owner is not asked to try again for a slip in the path data.
func TestSetupRetriesAnUnusableDrawingOnce(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		var request setupRequest
		json.NewDecoder(r.Body).Decode(&request)
		if calls.Add(1) == 1 {
			bad := fixtureProposal()
			bad["avatar"] = map[string]any{"background": "#10182a", "marks": []any{map[string]any{"d": "circle", "color": "#ffffff", "stroke_width": 0}}}
			writeSetupCall(w, "propose_identity", bad)
			return
		}
		if last := request.Messages[len(request.Messages)-1].Content; !strings.Contains(last, "mark 1 must be SVG path data") {
			t.Errorf("the retry did not say what was wrong: %q", last)
		}
		writeSetupCall(w, "propose_identity", fixtureProposal())
	})
	state, err := a.InterviewIdentity(context.Background(), SetupAssistant, "Draw them")
	if err != nil || state.Recommendation == nil || calls.Load() != 2 {
		t.Fatalf("retry: %+v %v after %d calls", state, err, calls.Load())
	}
	if len(state.Messages) != 2 || strings.Contains(state.Messages[1].Content, "could not be used") {
		t.Fatalf("the retry should not be kept in the interview: %+v", state.Messages)
	}
}

func TestSetupDemoDoesNotInvokeModel(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	a.Demo = true
	if _, err := a.InterviewIdentity(context.Background(), SetupAssistant, ""); err == nil {
		t.Fatal("demo started inference")
	}
	if calls.Load() != 0 {
		t.Fatal("demo contacted model")
	}
}
func TestSetupRefinementInvalidatesPreviousProposal(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeSetupCall(w, "propose_identity", fixtureProposal())
			return
		}
		writeSetupCall(w, "ask_setup_questions", map[string]any{"questions": []string{"Would you prefer a warmer palette?"}})
	})
	state, err := a.InterviewIdentity(context.Background(), SetupAssistant, "Suggest someone")
	if err != nil || state.Recommendation == nil {
		t.Fatal(state, err)
	}
	state, err = a.InterviewIdentity(context.Background(), SetupAssistant, "Please change the palette")
	if err != nil {
		t.Fatal(err)
	}
	if state.Recommendation != nil {
		t.Fatal("stale identity remained usable during refinement")
	}
}
func TestSetupHonorsDurableModelAllowance(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeSetupCall(w, "ask_setup_questions", map[string]any{"questions": []string{"What tone do you prefer?"}})
	})
	cfg := a.Config()
	cfg.Limits.MaxModelCallsPerDay = 1
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InterviewIdentity(context.Background(), SetupAssistant, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InterviewIdentity(context.Background(), SetupAssistant, "Calm"); err == nil {
		t.Fatal("setup bypassed daily model allowance")
	}
	if calls.Load() != 1 {
		t.Fatal("exhausted allowance still called model")
	}
}

func TestAppearanceIsTheOwnersAndSurvivesEarlierVersions(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	// A dashboard still running an earlier build saves a palette name.
	cfg := a.Config()
	cfg.Assistant.Theme = "charcoal-amber"
	if err := a.UpdateConfig(cfg); err != nil || a.Config().Assistant.Theme != config.ThemeSystem {
		t.Fatalf("theme %q %v", a.Config().Assistant.Theme, err)
	}
	cfg = a.Config()
	cfg.Assistant.Theme = config.ThemeLight
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Assistant.Theme != config.ThemeLight || snap.Assistant.Avatar.Shape != "orb" || !strings.HasPrefix(snap.Assistant.AvatarSVG, "<svg") {
		t.Fatalf("the dashboard does not see the appearance and avatar: %+v", snap.Assistant)
	}
}
