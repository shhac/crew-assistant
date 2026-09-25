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
	"github.com/shhac/crew-assistant/internal/testutil"
)

func setupModel(t *testing.T, a *App, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := testutil.NewServer(t, handler)
	t.Cleanup(server.Close)
	cfg := a.Config()
	cfg.Model.Engine = "openai-compatible"
	cfg.Model.Model = "fixture-model"
	cfg.Model.Effort = ""
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

func TestIdentityInterviewPreviewsThenAppliesOnlyAcceptedRecommendation(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct{ Role, Content string } `json:"messages"`
		}
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
		if calls.Add(1) == 1 {
			writeSetupCall(w, "ask_setup_questions", map[string]any{"questions": []string{"Should my tone be calm or energetic?", "Do you prefer a human or nature-inspired name?"}})
			return
		}
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "calm") {
			t.Error("owner preferences missing")
		}
		writeSetupCall(w, "propose_identity", fixtureProposal())
	})
	original := a.Config().Assistant
	state, err := a.InterviewIdentity(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Questions) != 2 || len(state.Messages) != 2 || state.Recommendation != nil {
		t.Fatalf("missing interview: %+v", state)
	}
	state, err = a.InterviewIdentity(ctx, "Keep it calm, with a nature-inspired name.")
	if err != nil {
		t.Fatal(err)
	}
	if state.Recommendation == nil || state.Recommendation.ID == "" || state.Recommendation.Applied || !strings.Contains(state.Recommendation.AvatarSVG, "<svg") {
		t.Fatalf("missing preview: %+v", state)
	}
	if !reflect.DeepEqual(a.Config().Assistant, original) {
		t.Fatal("recommendation applied without acceptance")
	}
	restored, err := a.IdentitySetup()
	if err != nil || restored.Recommendation.ID != state.Recommendation.ID {
		t.Fatal("setup did not persist", err)
	}
	info, err := os.Stat(a.configPath + ".identity-setup.json")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("setup is not private", err)
	}
	if _, err = a.ApplyIdentity(ctx, state.Recommendation.ID, false); err == nil {
		t.Fatal("implicit acceptance allowed")
	}
	if _, err = a.ApplyIdentity(ctx, "stale-proposal", true); err == nil {
		t.Fatal("stale recommendation applied")
	}
	// Applying a draft must preserve settings edited since the recommendation.
	cfg := a.Config()
	cfg.Limits.MaxModelTurns = 7
	if err = a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	painter := &fakePainter{}
	a.Painter = painter
	identity, err := a.ApplyIdentity(ctx, state.Recommendation.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	a.WaitForDrawings()
	if drawn := a.Config().Assistant.Avatar; drawn.Image == "" || drawn.Look != "Short silver hair and a green scarf" || !strings.Contains(painter.seen[0], "green scarf") {
		t.Fatalf("applying should have Codex draw the assistant: %+v", drawn)
	}
	if identity.Name != "Juniper" || len(identity.Avatar.Marks) != 2 || identity.Avatar.Marks[1].D != "M31 91 83 43" || identity.Avatar.Accent != "#91b5e8" || a.Config().Limits.MaxModelTurns != 7 {
		t.Fatal(identity)
	}
	persisted, err := config.Load(a.configPath)
	identity.Avatar.Image = a.Config().Assistant.Avatar.Image
	if err != nil || !reflect.DeepEqual(persisted.Assistant, identity) {
		t.Fatal("applied identity missing from config", err)
	}
	snap, err := a.Core.Snapshot(ctx)
	if err != nil || snap.Assistant.Name != "Juniper" {
		t.Fatal("runtime identity stale", err)
	}
	restored, err = a.IdentitySetup()
	if err != nil || !restored.Recommendation.Applied {
		t.Fatal("apply receipt missing", err)
	}
	if _, err = a.ApplyIdentity(ctx, state.Recommendation.ID, true); err != nil {
		t.Fatal("repeat apply should be idempotent", err)
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
			original := a.Config().Assistant
			if _, err := a.InterviewIdentity(context.Background(), "Suggest an identity"); err == nil {
				t.Fatal("unsafe setup result accepted")
			}
			state, err := a.IdentitySetup()
			if err != nil || state.Recommendation != nil {
				t.Fatal("unsafe proposal persisted", err)
			}
			snap, _ := a.Core.Snapshot(context.Background())
			if len(snap.Projects) != 0 || !reflect.DeepEqual(a.Config().Assistant, original) {
				t.Fatal("setup escaped identity scope")
			}
		})
	}
}

// A drawing that is not usable is sent back once with the reason, so the
// owner is not asked to try again for a slip in the path data.
// Whether to draw follows the state: an identity whose drawing failed is
// drawn when applied again, and one already drawn is left as it is.
func TestReapplyingAnIdentityDrawsItOnlyWhileItHasNoPicture(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		writeSetupCall(w, "propose_identity", fixtureProposal())
	})
	state, err := a.InterviewIdentity(ctx, "Choose your identity")
	if err != nil || state.Recommendation == nil {
		t.Fatal(state, err)
	}
	a.Painter = &fakePainter{fail: errors.New("no image tool")}
	if _, err := a.ApplyIdentity(ctx, state.Recommendation.ID, true); err != nil {
		t.Fatal(err)
	}
	a.WaitForDrawings()
	if snap, _ := a.Snapshot(ctx); !strings.Contains(snap.Assistant.DrawError, "no image tool") {
		t.Fatalf("the failed drawing should show: %+v", snap.Assistant)
	}
	painter := &fakePainter{}
	a.Painter = painter
	for i := 0; i < 2; i++ {
		if _, err := a.ApplyIdentity(ctx, state.Recommendation.ID, true); err != nil {
			t.Fatal(err)
		}
		a.WaitForDrawings()
	}
	if a.Config().Assistant.Avatar.Image == "" || len(painter.seen) != 1 {
		t.Fatalf("drawn %d times, picture %q", len(painter.seen), a.Config().Assistant.Avatar.Image)
	}
}

func TestSetupRetriesAnUnusableDrawingOnce(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	setupModel(t, a, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
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
	state, err := a.InterviewIdentity(context.Background(), "Draw yourself")
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
	if _, err := a.InterviewIdentity(context.Background(), ""); err == nil {
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
	state, err := a.InterviewIdentity(context.Background(), "Choose your identity")
	if err != nil {
		t.Fatal(err)
	}
	oldID := state.Recommendation.ID
	state, err = a.InterviewIdentity(context.Background(), "Please change the palette")
	if err != nil {
		t.Fatal(err)
	}
	if state.Recommendation != nil {
		t.Fatal("stale identity remained actionable during refinement")
	}
	if _, err = a.ApplyIdentity(context.Background(), oldID, true); err == nil {
		t.Fatal("replaced proposal applied")
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
	if _, err := a.InterviewIdentity(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.InterviewIdentity(context.Background(), "Calm"); err == nil {
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
	// A recommendation saved by an earlier version still names a palette;
	// applying it keeps the owner's own appearance.
	cfg = a.Config()
	cfg.Assistant.Theme = config.ThemeLight
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	legacy := `{"messages":[],"questions":[],"recommendation":{"id":"old","name":"Juniper","personality":"Calm","theme":"ink-blue","avatar":{"shape":"leaf","background":"#10182a","accent":"#91b5e8"},"rationale":"Calm","avatar_svg":"","applied":false}}`
	if err := os.WriteFile(a.configPath+".identity-setup.json", []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := a.ApplyIdentity(ctx, "old", true)
	if err != nil || identity.Name != "Juniper" || identity.Theme != config.ThemeLight {
		t.Fatalf("identity %+v %v", identity, err)
	}
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Assistant.Theme != config.ThemeLight || snap.Assistant.Avatar.Shape != "leaf" || !strings.HasPrefix(snap.Assistant.AvatarSVG, "<svg") {
		t.Fatalf("the dashboard does not see the appearance and avatar: %+v", snap.Assistant)
	}
}
