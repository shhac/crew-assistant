package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/engine"
)

func TestAssistantProfilesAreAddedChangedAndDeleted(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	stand := config.Avatar{Background: "#10182a", Accent: "#91b5e8", Marks: []config.Mark{{D: "M10 10 L50 50", Color: "#91b5e8"}}, Image: "0123456789abcdef0123456789abcdef", Look: "Short silver hair"}
	iris, err := a.CreateAssistant(ctx, AssistantInput{Name: " Iris ", Personality: "Warm.", Model: config.Model{Engine: "claude", Model: "opus", MaxTokens: 2048}, Avatar: &stand})
	if err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	saved, _ := a.Config().Profile(iris.ID)
	if saved.Name != "Iris" || saved.Personality != "Warm." || saved.Model.Model != "opus" || len(saved.Avatar.Marks) != 1 {
		t.Fatalf("%+v", saved)
	}
	// A picture is only ever drawn, never sent: the look is drawn instead.
	if saved.Avatar.Image == stand.Image || saved.Avatar.Image == "" || len(painter.seen) != 1 || !strings.Contains(painter.seen[0], "Short silver hair") {
		t.Fatalf("%+v %q", saved.Avatar, painter.seen)
	}
	if a.Config().Assistant.Seat != "milo" {
		t.Fatal("a new assistant took the seat; the owner chooses who does")
	}
	if _, err := a.CreateAssistant(ctx, AssistantInput{Name: "quill", Model: config.DefaultProfile().Model}); err == nil {
		t.Fatal("two assistants share a name")
	}
	changed, err := a.SaveAssistant(ctx, iris.ID, AssistantInput{Name: "Iris", Personality: "Exact.", Model: config.Model{Engine: "codex", Model: "gpt-6-astra", Effort: "high", MaxTokens: 4096}, Avatar: &config.Avatar{Shape: "leaf", Background: "#000000", Accent: "#ffffff"}})
	if err != nil || changed.Personality != "Exact." || changed.Model.Engine != "codex" || changed.Avatar.Image != saved.Avatar.Image || changed.Avatar.Look != "Short silver hair" {
		t.Fatalf("%+v %v", changed, err)
	}
	browsing, err := a.SaveAssistant(ctx, iris.ID, AssistantInput{Name: "Iris", Personality: "Exact.", Model: changed.Model, Browser: &config.Browser{On: true, Name: " Work "}})
	if err != nil || browsing.Browser != (config.Browser{On: true, Name: "Work"}) {
		t.Fatalf("%+v %v", browsing.Browser, err)
	}
	if kept, err := a.SaveAssistant(ctx, iris.ID, AssistantInput{Name: "Iris", Personality: "Exact.", Model: changed.Model}); err != nil || !kept.Browser.On {
		t.Fatalf("a save that didn't mention the browser changed it: %+v %v", kept.Browser, err)
	}
	if _, err := a.SaveAssistant(ctx, iris.ID, AssistantInput{Name: "Iris", Model: config.Model{Engine: "grok", Model: "grok-5", MaxTokens: 4096}}); err == nil || !strings.Contains(err.Error(), "browser") {
		t.Fatalf("an assistant on an engine without the browser kept it: %v", err)
	}
	if _, err := a.SaveAssistant(ctx, "nobody", AssistantInput{Name: "X", Model: config.DefaultProfile().Model}); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	persisted, err := config.Load(a.configPath)
	if p, _ := persisted.Profile(iris.ID); err != nil || p.Personality != "Exact." {
		t.Fatalf("%+v %v", persisted.Assistants, err)
	}
	if err := a.DeleteAssistant(ctx, iris.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Config().Profile(iris.ID); ok || a.Config().Assistant.Seat != "milo" {
		t.Fatalf("%+v", a.Config().Assistant)
	}
	if err := a.DeleteAssistant(ctx, iris.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
}

// What an assistant remembers about the owner, every assistant reads; what
// it remembers about itself only it does, and goes when it is deleted.
func TestEachAssistantKeepsItsOwnMemoriesAndSharesTheOwners(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	remember := func(key, value, about string) error {
		raw, _ := json.Marshal(engine.PreferenceArgs{Key: key, Value: value, About: about})
		_, err := a.Execute(ctx, "remember_preference", raw)
		return err
	}
	if err := remember("tone", "The owner likes short answers.", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := remember("habit", "I open with the outcome.", "yourself"); err != nil {
		t.Fatal(err)
	}
	if err := remember("x", "y", "someone"); err == nil {
		t.Fatal("a memory about someone else was kept")
	}
	iris, err := a.CreateAssistant(ctx, AssistantInput{Name: "Iris", Model: config.DefaultProfile().Model})
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.Config()
	cfg.Assistant.Seat = iris.ID
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := remember("habit", "I ask before I act.", "yourself"); err != nil {
		t.Fatal(err)
	}
	seen := func() string {
		raw, _, err := a.chatContext(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if view := seen(); !strings.Contains(view, "short answers") || !strings.Contains(view, "ask before I act") || strings.Contains(view, "open with the outcome") {
		t.Fatalf("Iris sees %s", view)
	}
	cfg = a.Config()
	cfg.Assistant.Seat = "milo"
	if err := a.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if view := seen(); !strings.Contains(view, "short answers") || strings.Contains(view, "ask before I act") || !strings.Contains(view, "open with the outcome") {
		t.Fatalf("Quill sees %s", view)
	}
	snap, _ := a.Snapshot(ctx)
	if len(snap.Memories) != 3 {
		t.Fatalf("the owner should see every memory: %+v", snap.Memories)
	}
	if err := a.DeleteAssistant(ctx, iris.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = a.Snapshot(ctx)
	for _, m := range snap.Memories {
		if m.Assistant == iris.ID {
			t.Fatalf("a deleted assistant's memory stayed: %+v", m)
		}
	}
	if len(snap.Memories) != 2 {
		t.Fatalf("%+v", snap.Memories)
	}
}

// Other assistants and their faces are the owner's screen's, not the
// assistant's turns.
func TestTheAssistantDoesNotSeeTheOtherProfiles(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	if _, err := a.CreateAssistant(ctx, AssistantInput{Name: "Iris", Personality: "Secretive", Model: config.DefaultProfile().Model}); err != nil {
		t.Fatal(err)
	}
	raw, _, err := a.chatContext(ctx, "")
	if err != nil || strings.Contains(string(raw), "Iris") || strings.Contains(string(raw), "Secretive") {
		t.Fatalf("%s %v", raw, err)
	}
}

// A member's personality is kept, and a look sent with a new member is what
// is drawn.
func TestAMembersPersonalityAndSuggestedLook(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	avatar := config.Avatar{Background: "#10182a", Accent: "#91b5e8", Marks: []config.Mark{{D: "M10 10 L50 50", Color: "#91b5e8"}}, Look: "Round glasses"}
	m, err := a.CreateMember(ctx, core.MemberInput{Name: "Moss", Kinds: []string{core.RoleReviewer}, Engine: "codex", Personality: "Dry wit.", Avatar: &avatar})
	if err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	snap, _ := a.Snapshot(ctx)
	got, _ := snap.Member(m.ID)
	if got.Personality != "Dry wit." || got.Avatar.Look != "Round glasses" || got.Avatar.Image == "" || !strings.Contains(painter.seen[0], "Round glasses") || !strings.Contains(painter.seen[0], "Dry wit.") {
		t.Fatalf("%+v %q", got, painter.seen)
	}
	if _, err := a.Core.SaveMember(ctx, m.ID, core.MemberInput{Name: "Moss", Kinds: []string{core.RoleReviewer}, Engine: "codex", Personality: strings.Repeat("x", core.MaxPersonality+1)}); err == nil {
		t.Fatal("an overlong personality was kept")
	}
}
