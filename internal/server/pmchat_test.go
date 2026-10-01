package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestProjectPMChatHTTP(t *testing.T) {
	s, call := ownerServer(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, core.ProjectInput{Title: "Test", Template: "draft", Brief: core.BriefInput{Goal: "Test"}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/projects/" + p.ID + "/pm-chat"
	if w := call("POST", base+"/messages", `{"id":"msg","text":"Question"}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	pb := *p.Playbook
	pb.Roles = append(pb.Roles, core.Role{Name: "Pim", Kinds: []string{core.RolePM}, Engine: "claude"})
	if _, err = s.SetPlaybook(ctx, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if w := call("POST", base+"/messages", `{"id":"msg","text":"Question"}`); w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call("GET", base, "")
	var log struct {
		Messages  []core.PMChatMessage `json:"messages"`
		AvatarSVG string               `json:"avatar_svg"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &log) != nil || len(log.Messages) != 1 {
		t.Fatal(w.Body.String())
	}
	wantSVG, _ := config.DefaultAvatar("Pim").SVG()
	if log.AvatarSVG != wantSVG {
		t.Fatal("memberless PM did not use its deterministic preset", log.AvatarSVG)
	}
	snap, _ := s.Snapshot(ctx)
	if len(snap.Members) != 0 {
		t.Fatal("display fallback created a member")
	}
	m, c, seat, ok, err := s.ClaimPMChat(ctx, p.ID, func(core.Role) string { return "" })
	if !ok || err != nil {
		t.Fatal(err)
	}
	if err = s.AnswerPMChat(ctx, p.ID, m.ID, c.Token, seat.Name, "Reply", nil); err != nil {
		t.Fatal(err)
	}
	s.ReleaseProjectClaim(ctx, p.ID, c.Token)
	if w = call("GET", base, ""); !strings.Contains(w.Body.String(), "Reply") || strings.Contains(w.Body.String(), c.Token) {
		t.Fatal(w.Body.String())
	}
	s.SendPMChat(ctx, p.ID, "failed", "Another question")
	m, c, _, _, _ = s.ClaimPMChat(ctx, p.ID, func(core.Role) string { return "" })
	s.FailPMChat(ctx, p.ID, m.ID, c.Token, "Safe reason", nil)
	s.ReleaseProjectClaim(ctx, p.ID, c.Token)
	for i := 0; i < 2; i++ {
		if w = call("POST", base+"/messages/failed/retry", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "waiting") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w = call("GET", "/api/projects/missing/pm-chat", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = call("POST", base+"/messages", `{"id":"invalid","text":""}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
}
