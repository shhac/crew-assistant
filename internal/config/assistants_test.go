package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The assistant of a file from before profiles becomes the first profile,
// in the seat, with everything the owner had set.
func TestAnEarlierAssistantBecomesTheSeatedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{
  "assistant": {"name": "Juniper Bell", "personality": "Dry and brief.", "theme": "dark",
                "avatar": {"shape": "leaf", "background": "#101010", "accent": "#fafafa", "look": "short grey hair"}},
  "model": {"engine": "claude", "model": "opus", "effort": "high", "max_tokens": 8192, "claude_home": "/synthetic/claude"}
}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Assistants) != 1 || c.Assistant.Seat != "juniper-bell" || c.Assistant.Theme != ThemeDark {
		t.Fatalf("%+v %+v", c.Assistant, c.Assistants)
	}
	p, ok := c.Seated()
	if !ok || p.Name != "Juniper Bell" || p.Personality != "Dry and brief." || p.Avatar.Shape != "leaf" || p.Avatar.Look != "short grey hair" {
		t.Fatalf("%+v", p)
	}
	if p.Model != (Model{Engine: "claude", Model: "opus", Effort: "high", MaxTokens: 8192}) {
		t.Fatalf("%+v", p.Model)
	}
	if h := c.AssistantHarness(); h.Engine != "claude" || h.Model != "opus" || h.MaxTokens != 8192 || h.Home != "/synthetic/claude" {
		t.Fatalf("%+v", h)
	}
	unknown := UnknownKeys(path)
	if len(unknown) == 0 {
		t.Fatal("an unconverted file reported nothing")
	}
	for _, k := range unknown {
		if !k.Renamed || !strings.HasPrefix(k.To, "assistants") && !strings.HasPrefix(k.To, "engines") {
			t.Errorf("%s: %s", k.Path, k)
		}
	}
	if changed, err := Upgrade(path); err != nil || !changed {
		t.Fatal(changed, err)
	}
	again, err := Load(path)
	if err != nil || again.Assistant.Seat != c.Assistant.Seat || !reflect.DeepEqual(again.Assistants, c.Assistants) {
		t.Fatalf("the rewritten file reads differently: %+v %v", again.Assistants, err)
	}
	if len(UnknownKeys(path)) != 0 {
		t.Fatalf("keys left over: %+v", UnknownKeys(path))
	}
}

// A file with only settings that aren't the assistant's keeps the default
// assistant, seated.
func TestAFileWithoutAnAssistantKeepsTheDefault(t *testing.T) {
	doc := map[string]any{"assistant": map[string]any{"theme": "light"}, "limits": map[string]any{"max_model_turns": 4.0}}
	if convertProfile(doc) {
		t.Fatalf("converted %v", doc)
	}
	c := Default()
	if p, ok := c.Seated(); !ok || p.Name != DefaultAssistantName || p.ID != "milo" {
		t.Fatalf("%+v", c.Assistant)
	}
}

// Saved assistants are read as they are: none of the default's settings
// fill in what one leaves out, and an empty list stays empty.
func TestSavedAssistantsReplaceTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	saved := `{"assistant":{"seat":"iris"},"assistants":[{"id":"iris","name":"Iris","avatar":{"shape":"orb","background":"#000000","accent":"#ffffff"},"model":{"engine":"codex","model":"","effort":"","max_tokens":1024}}]}`
	if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Assistants) != 1 || c.Assistants[0].Personality != "" || c.Assistants[0].Model.Model != "" {
		t.Fatalf("the default was read into a saved assistant: %+v", c.Assistants)
	}
	if err := os.WriteFile(path, []byte(`{"assistants":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(path); err != nil || len(c.Assistants) != 0 || c.Assistant.Seat != "" {
		t.Fatalf("no assistants became %+v %+v %v", c.Assistants, c.Assistant, err)
	}
	if h := c.AssistantHarness(); h.Engine != "" {
		t.Fatalf("an empty seat has a model: %+v", h)
	}
}

func TestAssistantProfilesAreValidatedTogether(t *testing.T) {
	second := func(c *Config) *AssistantProfile {
		p := DefaultProfile()
		p.ID, p.Name = "iris", "Iris"
		c.Assistants = append(c.Assistants, p)
		return &c.Assistants[1]
	}
	ok := Default()
	second(&ok)
	ok.Assistant.Seat = "iris"
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	ok.Assistant.Seat = ""
	if err := ok.Validate(); err != nil {
		t.Fatalf("an empty seat was refused: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"same id":          func(c *Config) { second(c).ID = "milo" },
		"same name":        func(c *Config) { second(c).Name = "MILO" },
		"bad id":           func(c *Config) { second(c).ID = "Iris!" },
		"no name":          func(c *Config) { second(c).Name = " " },
		"long personality": func(c *Config) { second(c).Personality = strings.Repeat("x", 4001) },
		"bad model":        func(c *Config) { second(c).Model.MaxTokens = 1 },
		"browser on grok":  func(c *Config) { p := second(c); p.Model.Engine, p.Browser = "grok", Browser{On: true} },
		"browser name":     func(c *Config) { second(c).Browser = Browser{On: true, Name: "Work\nHome"} },
		"unknown seat":     func(c *Config) { c.Assistant.Seat = "nobody" },
	} {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestProfileIDsComeFromNames(t *testing.T) {
	for name, want := range map[string]string{"Milo": "milo", " Juniper  Bell ": "juniper-bell", "Zoë": "zo", "!!": "assistant", strings.Repeat("a", 70): strings.Repeat("a", 64)} {
		if got := ProfileID(name); got != want || !profileID.MatchString(got) {
			t.Errorf("%q became %q", name, got)
		}
	}
	if id := NewProfileID(); !profileID.MatchString(id) || id == NewProfileID() {
		t.Fatal(id)
	}
}
