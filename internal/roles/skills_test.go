package roles

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/bundledskills"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func TestBundledSkillOptionsForNativeEngines(t *testing.T) {
	set, digest, err := bundledskills.Prepare(t.TempDir(), "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"codex", "claude"} {
		spec := Spec{Engine: engine, Skills: set, Instructions: bundledskills.Fingerprint(digest), WorkDir: t.TempDir()}
		o := options(spec)
		if len(o.Skills.Provided) != 1 || o.Skills.Delivery != harness.SkillDeliveryComposed || o.Skills.Global != harness.GlobalSkillsDefault || o.Skills.Provided[0].Scripts || o.Instructions.Text != spec.Instructions {
			t.Fatal(engine, o.Skills)
		}
	}
}

// Exercise actual harness ConfigHash and Resume without inference or sockets.
func TestBundledSkillRealResumeAcrossRoundsRestartAndContentChange(t *testing.T) {
	ctx := context.Background()
	state, work, home := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	set, digest, err := bundledskills.Prepare(state, "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) { return session.ToolResult{}, nil }), Engine: "openai-compatible", Model: "fake", WorkDir: work, RuntimeHome: home, Skills: set, Instructions: bundledskills.Fingerprint(digest), Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}}
	o := options(spec)
	o.Workbench.Commands = nil
	s, opened, err := open(ctx, o, nil)
	if err != nil || opened.Resumed {
		t.Fatal(opened, err)
	}
	ref := s.Ref()
	if _, err := s.Release(ctx); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ref)
	for _, disabled := range [][]string{nil, {"sprite-atlas-pipeline"}} {
		set, next, err := bundledskills.Prepare(state, "designer", disabled)
		if err != nil {
			t.Fatal(err)
		}
		spec.Skills, spec.Instructions = set, bundledskills.Fingerprint(next)
		changed := options(spec)
		changed.Workbench.Commands = nil
		resumed, actual, err := open(ctx, changed, raw)
		if err != nil || !actual.Resumed || resumed.Ref().ConfigHash != ref.ConfigHash {
			t.Fatal(actual, err)
		}
		if _, err := resumed.Release(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a new embedded content revision in another immutable directory.
	content, err := os.ReadFile(filepath.Join(set.Provided[0].Dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, []byte("\nUpdated canonical guidance.\n")...)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0400); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00", set.Provided[0].Name, len(content))
	h.Write(content)
	spec.Skills.Provided = slices.Clone(set.Provided)
	spec.Skills.Provided[0].Dir = dir
	spec.Instructions = bundledskills.Fingerprint(fmt.Sprintf("%x", h.Sum(nil)))
	changed := options(spec)
	changed.Workbench.Commands = nil
	fresh, actual, err := open(ctx, changed, raw)
	if err != nil || actual.Resumed || actual.Fresh != session.FreshIncompatible || fresh.Ref().ConfigHash == ref.ConfigHash {
		t.Fatal(actual, err)
	}
	if _, err := fresh.Release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBundledSkillHostedToolsAreReadOnlyAndHaveNoScripts(t *testing.T) {
	set, _, err := bundledskills.Prepare(t.TempDir(), "designer", nil)
	if err != nil {
		t.Fatal(err)
	}
	definitions, handler, err := session.SkillTools(set, session.SkillRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || definitions[0].Name != "load_skill" {
		t.Fatal(definitions)
	}
	reply, err := handler.CallTool(context.Background(), session.ToolCall{Name: "load_skill", Arguments: json.RawMessage(`{"skill":"sprite-atlas"}`)})
	if err != nil || reply.IsError || !strings.Contains(reply.Content, "canonical base image") {
		t.Fatal(reply, err)
	}
	reply, err = handler.CallTool(context.Background(), session.ToolCall{Name: "run_skill_script", Arguments: json.RawMessage(`{"skill":"sprite-atlas","script":"anything"}`)})
	if err != nil || !reply.IsError || !strings.Contains(reply.Content, "scripts_not_permitted") {
		t.Fatal("script request accepted", reply, err)
	}
	// A malformed enabled skill is an admission failure, never silently omitted.
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	o := options(Spec{Tools: []session.ToolDefinition{{Name: "read_task", Schema: map[string]any{"type": "object"}}}, Handler: session.ToolHandlerFunc(func(context.Context, session.ToolCall) (session.ToolResult, error) { return session.ToolResult{}, nil }), Engine: "openai-compatible", Model: "fake", WorkDir: t.TempDir(), RuntimeHome: home, Skills: harness.Skills{Delivery: harness.SkillDeliveryComposed, Provided: []harness.Skill{{Name: "sprite-atlas", Dir: t.TempDir()}}}, Provider: harness.Provider{Engine: harness.OpenAICompatible, API: harness.API{BaseURL: "http://127.0.0.1:1/v1", Unauthenticated: true, Dialect: harness.OpenAIChatCompletions}}})
	o.Workbench.Commands = nil
	if _, _, err := open(context.Background(), o, nil); err == nil {
		t.Fatal("admitted missing skill")
	}
	o.Skills = harness.Skills{}
	s, _, err := open(context.Background(), o, nil)
	if err != nil {
		t.Fatal("control fixture was not otherwise admissible", err)
	}
	if _, err := s.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}
