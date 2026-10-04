package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/bundledskills"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestOwnerBundledSkillCatalogAndProjectToggle(t *testing.T) {
	s, call := ownerServer(t)
	w := call("GET", "/api/bundled-skills", "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var catalog []bundledskills.Entry
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || len(catalog) != 2 {
		t.Fatal(catalog, err)
	}
	p := call("POST", "/api/projects", `{"title":"Sprites","brief":{"goal":"Animate","criteria":["Consistent"]},"template":"draft"}`)
	if p.Code != 201 {
		t.Fatal(p.Body)
	}
	var project core.Project
	json.Unmarshal(p.Body.Bytes(), &project)
	path := "/api/projects/" + project.ID + "/bundled-skills/sprite-atlas"
	w = call("PUT", path, `{"enabled":false}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	snap, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snap.Projects {
		if p.ID == project.ID && (len(p.Playbook.DisabledBundledSkills) != 1 || p.Playbook.DisabledBundledSkills[0] != "sprite-atlas") {
			t.Fatal(p)
		}
	}
	if w := call("PUT", "/api/projects/"+project.ID+"/bundled-skills/missing", `{"enabled":false}`); w.Code == 200 {
		t.Fatal("invalid skill accepted")
	}
	if w := call("PUT", path, `{"enabled":true}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
}

func TestBundledSkillRoutesRequireOwnerAuthentication(t *testing.T) {
	_, auth, h := newDashboard(t, config.Default())
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/api/bundled-skills", ""},
		{"PUT", "/api/projects/p1/bundled-skills/sprite-atlas", `{"enabled":false}`},
	} {
		w := send(h, auth, req.method, req.path, strings.NewReader(req.body), caller{csrf: true})
		if w.Code != 401 {
			t.Fatal("anonymous skill control", req.path, w.Code)
		}
	}
}

func TestSpriteRetirementUsesExistingMemberPUT(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	in := core.MemberInput{Name: "Ash", Kinds: []string{core.RoleDesigner}, Engine: "codex", Instructions: core.LegacySpriteInstructions}
	m, err := s.SaveMember(ctx, "", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Instructions = ""
	expected := core.LegacySpriteInstructions
	in.ExpectedInstructions = &expected
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	w := call("PUT", "/api/members/"+m.ID, string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seat := core.Role{Member: m.ID, Instructions: core.LegacySpriteInstructions}
	if snap.EffectiveSeatInstructions(seat) != "" {
		t.Fatal("PUT failed to record retirement")
	}
	if w := call("POST", "/api/members/retire-sprite-lessons", `{}`); w.Code == 200 {
		t.Fatal("one-off endpoint remains")
	}
}

func TestMemberPUTRejectsStaleInstructionsPrecondition(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	in := core.MemberInput{Name: "Ash", Kinds: []string{core.RoleDesigner}, Engine: "codex", Instructions: core.LegacySpriteInstructions, Personality: "Original personality"}
	m, err := s.SaveMember(ctx, "", in)
	if err != nil {
		t.Fatal(err)
	}
	expected := m.Instructions
	stale := in
	stale.Instructions, stale.ExpectedInstructions = "", &expected
	in.Instructions, in.Personality = "Owner edit", "Updated personality"
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/members/" + m.ID
	if w := call("PUT", path, string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", path, string(body)); w.Code != 409 {
		t.Fatal("stale PUT accepted", w.Code, w.Body)
	}
	after, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("conflicting PUT changed state")
	}
}

func TestConditionalMemberPUTPreservesInterveningUnrelatedFields(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	in := core.MemberInput{Name: "Ash", Kinds: []string{core.RoleDesigner}, Engine: "codex", Instructions: core.LegacySpriteInstructions, Personality: "Before"}
	m, err := s.SaveMember(ctx, "", in)
	if err != nil {
		t.Fatal(err)
	}
	expected := m.Instructions
	stale := in
	stale.Instructions, stale.ExpectedInstructions = "", &expected
	in.Name, in.Personality, in.Description, in.Effort = "Ember", "Current personality", "Current description", "high"
	in.Kinds = []string{core.RoleImplementer}
	in.Browser.On = true
	path := "/api/members/" + m.ID
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if w := call("PUT", path, string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := before.Member(m.ID)
	body, err = json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	w := call("PUT", path, string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var got core.Member
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	current.Instructions = ""
	// Snapshot decorates avatars for display; SaveMember returns stored fields.
	got.AvatarSVG = current.AvatarSVG
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("cleanup overwrote unrelated fields: got %+v; want %+v", got, current)
	}
	after, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := after.Member(m.ID)
	if !reflect.DeepEqual(saved, current) {
		t.Fatal("response differs from persisted fields")
	}
	if after.EffectiveSeatInstructions(core.Role{Member: m.ID, Instructions: core.LegacySpriteInstructions}) != "" {
		t.Fatal("role removal prevented retirement")
	}
}
