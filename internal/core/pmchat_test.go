package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
)

func TestPMChatValidationQueueAndIdempotency(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	plain := newProject(t, s)
	for _, text := range []string{"", "   ", strings.Repeat("x", 24001)} {
		if _, err := s.SendPMChat(testContext, p.ID, "a", text); !errors.Is(err, ErrChatValidation) {
			t.Fatal(err)
		}
	}
	if _, err := s.SendPMChat(testContext, p.ID, "bad/id", "hello"); !errors.Is(err, ErrChatValidation) {
		t.Fatal(err)
	}
	if _, err := s.SendPMChat(testContext, plain.ID, "a", "hello"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.SendPMChat(testContext, "missing", "a", "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.SendPMChat(testContext, p.ID, fmt.Sprint(i), "hello"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SendPMChat(testContext, p.ID, "0", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendPMChat(testContext, p.ID, "0", "changed"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.SendPMChat(testContext, p.ID, "6", "hello"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestPMChatClaimFenceRetryAndRecovery(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	s.SendPMChat(testContext, p.ID, "first", "hello")
	s.SendPMChat(testContext, p.ID, "second", "later")
	look, _, ok, err := s.ClaimPMQuestion(testContext, p.ID, anyone)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, _, _, ok, err := s.ClaimPMChat(testContext, p.ID, anyone); ok || err != nil {
		t.Fatal("claimed busy PM", err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, look.Token)
	m, c, seat, ok, err := s.ClaimPMChat(testContext, p.ID, anyone)
	if err != nil || !ok || m.ID != "first" || m.Status != "working" || seat.Name != "Pim" {
		t.Fatalf("%+v %v", m, err)
	}
	if _, _, ok, err := s.ClaimPMQuestion(testContext, p.ID, anyone); ok || err != nil {
		t.Fatal("ask_pm claimed busy chat")
	}
	if err := s.AnswerPMChat(testContext, p.ID, m.ID, "stale", seat.Name, "reply", nil); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := s.FailPMChat(testContext, p.ID, m.ID, "stale", "failed", nil); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := s.RecoverClaims(testContext, map[string]string{c.Token: "held"}); err != nil {
		t.Fatal(err)
	}
	log, _ := s.PMChat(testContext, p.ID)
	if log[0].Status != "working" {
		t.Fatal(log)
	}
	if err := s.RecoverClaims(testContext, nil); err != nil {
		t.Fatal(err)
	}
	log, _ = s.PMChat(testContext, p.ID)
	if log[0].Status != "failed" || log[0].Error != "Stopped before Pim replied" {
		t.Fatal(log)
	}
	for i := 0; i < 2; i++ {
		m, err = s.RetryPMChat(testContext, p.ID, "first")
		if err != nil || m.Status != "waiting" {
			t.Fatal(m, err)
		}
	}
	m, c, seat, ok, err = s.ClaimPMChat(testContext, p.ID, anyone)
	if !ok || err != nil {
		t.Fatal(err)
	}
	receipt := []PMChatChange{{Kind: "updated", Summary: "Updated a request", Tasks: []string{"t"}}}
	if err = s.FailPMChat(testContext, p.ID, m.ID, c.Token, "safe failure", receipt); err != nil {
		t.Fatal(err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	log, _ = s.PMChat(testContext, p.ID)
	if len(log[0].Changes) != 1 {
		t.Fatal(log)
	}
	s.RetryPMChat(testContext, p.ID, m.ID)
	m, c, seat, _, _ = s.ClaimPMChat(testContext, p.ID, anyone)
	if err = s.AnswerPMChat(testContext, p.ID, m.ID, c.Token, seat.Name, "reply", receipt); err != nil {
		t.Fatal(err)
	}
	log, _ = s.PMChat(testContext, p.ID)
	if len(log) != 3 || log[2].Text != "reply" || log[2].ReplyTo != "first" {
		t.Fatal(log)
	}
	if _, err = s.RetryPMChat(testContext, p.ID, m.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	if err = s.AnswerPMChat(testContext, p.ID, m.ID, c.Token, seat.Name, "late", nil); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
func TestPMChatPersistenceRetentionAndIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, config.Default())
	p := pmProject(t, s)
	other := pmProject(t, s)
	s.SendPMChat(testContext, other.ID, "other", "other project")
	for i := 0; i < 105; i++ {
		id := fmt.Sprint(i)
		if _, err = s.SendPMChat(testContext, p.ID, id, "hello"); err != nil {
			t.Fatal(err)
		}
		m, c, seat, ok, e := s.ClaimPMChat(testContext, p.ID, anyone)
		if e != nil || !ok {
			t.Fatal(e)
		}
		if e = s.AnswerPMChat(testContext, p.ID, m.ID, c.Token, seat.Name, "reply", nil); e != nil {
			t.Fatal(e)
		}
		s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, config.Default())
	log, _ := s.PMChat(testContext, p.ID)
	if len(log) != 200 || log[len(log)-1].Text != "reply" {
		t.Fatal(len(log))
	}
	log, _ = s.PMChat(testContext, other.ID)
	if len(log) != 1 || log[0].Text != "other project" {
		t.Fatal(log)
	}
}
func TestPMRemovedWhileChatWaits(t *testing.T) {
	s, _ := fixture(t)
	p := pmProject(t, s)
	s.SendPMChat(testContext, p.ID, "a", "hello")
	pb := *p.Playbook
	pb.Roles = pb.Roles[:len(pb.Roles)-1]
	if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	s.ClaimPMChat(testContext, p.ID, anyone)
	log, _ := s.PMChat(testContext, p.ID)
	if log[0].Status != "failed" || log[0].Error != "This project has no PM now" {
		t.Fatal(log)
	}
}

func TestPMChatHeldClaimSurvivesReopenAndReleaseFailsMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(store, config.Default())
	p := pmProject(t, s)
	s.SendPMChat(testContext, p.ID, "msg", "hello")
	_, c, _, ok, err := s.ClaimPMChat(testContext, p.ID, anyone)
	if !ok || err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(store, config.Default())
	s.RecoverClaims(testContext, map[string]string{c.Token: "still running"})
	log, _ := s.PMChat(testContext, p.ID)
	if log[0].Status != "working" {
		t.Fatal(log)
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	log, _ = s.PMChat(testContext, p.ID)
	if log[0].Status != "failed" || log[0].Error != "Stopped before Pim replied" {
		t.Fatal(log)
	}
}

func TestPMChatKeepsThePersonBusyAcrossProjects(t *testing.T) {
	s, _ := fixture(t)
	pm := Role{Name: "Pim", Kinds: []string{RolePM}, Engine: "claude", Member: "same-person"}
	a, b := newProject(t, s), newProject(t, s)
	a = seated(t, s, a, 0, a.Playbook.Roles[0], pm)
	b = seated(t, s, b, 0, b.Playbook.Roles[0], pm)
	s.SendPMChat(testContext, a.ID, "a", "hello")
	s.SendPMChat(testContext, b.ID, "b", "hello")
	_, c, _, ok, err := s.ClaimPMChat(testContext, a.ID, anyone)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if _, _, _, ok, err = s.ClaimPMChat(testContext, b.ID, anyone); err != nil || ok {
		t.Fatal("person worked twice", err)
	}
	s.ReleaseProjectClaim(testContext, a.ID, c.Token)
	if _, _, _, ok, err = s.ClaimPMChat(testContext, b.ID, anyone); err != nil || !ok {
		t.Fatal("person stayed busy", err)
	}
}
