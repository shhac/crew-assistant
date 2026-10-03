package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestReleasePolicyValidation(t *testing.T) {
	pb := Playbook{Medium: MediumGit, Land: LandPolicy{Via: LandPush, Target: "main"}}
	valid := ReleasePolicy{When: "after a feature", Check: "make release-check VERSION={version}"}
	if err := valid.validate(pb); err != nil {
		t.Fatal(err)
	}
	cases := []ReleasePolicy{{}, {When: strings.Repeat("x", 1001)}, {When: "yes", Check: "one\ntwo"}, {When: "yes", Check: strings.Repeat("x", 501)}, {When: "yes", Approve: "none"}, {When: "yes", GitHub: "bad"}}
	for _, r := range cases {
		if err := r.validate(pb); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	for _, medium := range []string{MediumDocuments, MediumGit} {
		bad := pb
		bad.Medium = medium
		bad.Land = LandPolicy{}
		if valid.validate(bad) == nil {
			t.Fatal("accepted draft/branch")
		}
	}
	pb.Land = LandPolicy{PullRequests: true, GitHub: "owner/repo", Target: "main"}
	valid.GitHub = "other/repo"
	if valid.validate(pb) == nil {
		t.Fatal("PR accepted repository override")
	}
}
func TestReleaseVersions(t *testing.T) {
	for _, v := range []string{"1", "v1.2", "01.2.3", "v1.2.3-01", "v1.2.3-", "v1.2.3+", "v1.2.3..4"} {
		if ValidVersion(v) {
			t.Fatal("accepted", v)
		}
	}
	for _, c := range []struct{ in, latest, want string }{{"1.1.0", "v1.0.0", "v1.1.0"}, {"v2.0.0", "1.0.0", "2.0.0"}, {"1.0.0", "", "v1.0.0"}, {"v1.0.0", "v1.0.0-rc.2", "v1.0.0"}} {
		got, err := ReleaseVersion(c.in, c.latest)
		if err != nil || got != c.want {
			t.Fatal(got, err)
		}
	}
	if _, err := ReleaseVersion("v1.0.0+different", "v1.0.0"); err == nil {
		t.Fatal("metadata advanced version")
	}
	if CompareVersions("v1.0.0-rc.10", "v1.0.0-rc.2") <= 0 {
		t.Fatal("lexical ordering")
	}
}
func releaseProject(t *testing.T, s *Service, approve string) Project {
	t.Helper()
	p := newProject(t, s)
	pb := Templates["code"]
	pb.Repo = t.TempDir()
	pb.Check = "make check"
	pb.Land = LandPolicy{Via: LandPush, Target: "main"}
	pb.Release = &ReleasePolicy{When: "after a feature", Approve: approve}
	var err error
	p, err = s.SetPlaybook(testContext, p.ID, pb)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func proposal() PMAnswer {
	return PMAnswer{Release: &ReleaseProposal{Version: "v1.1.0", Notes: "Adds a feature."}, ReleaseContext: ReleaseContext{Latest: "v1.0.0", Commits: []string{"abc Adds a feature"}, Count: 1}}
}
func currentRelease(t *testing.T, s *Service, id string) *ReleaseRun {
	t.Helper()
	snap, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := findProjectByID(snap, id)
	return p.Release
}
func TestReleaseProposalsAndDecisions(t *testing.T) {
	s, _ := fixture(t)
	p := releaseProject(t, s, "")
	in := proposal()
	if _, err := s.ApplyPM(testContext, p.ID, in); err != nil {
		t.Fatal(err)
	}
	r := currentRelease(t, s, p.ID)
	if r == nil || r.State != "proposed" || r.DecisionID == "" {
		t.Fatal(r)
	}
	if _, err := s.ApplyPM(testContext, p.ID, in); err != nil {
		t.Fatal(err)
	}
	if currentRelease(t, s, p.ID).DecisionID != r.DecisionID {
		t.Fatal("second proposal replaced pending")
	}
	if _, err := s.AnswerDecision(testContext, r.DecisionID, "Release v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if currentRelease(t, s, p.ID) != nil {
		t.Fatal("free text approved")
	}
	// Open a fresh proposal: custom answers cannot approve.
	s.ApplyPM(testContext, p.ID, in)
	r = currentRelease(t, s, p.ID)
	if _, err := s.ChooseDecision(testContext, r.DecisionID, "Release v1.1.0"); err != nil {
		t.Fatal(err)
	}
	if currentRelease(t, s, p.ID).ApprovedBy != "owner" {
		t.Fatal("not approved")
	}
	if err := s.ReleaseFailed(testContext, p.ID, "exit 3\nfailed", false); err != nil {
		t.Fatal(err)
	}
	r = currentRelease(t, s, p.ID)
	s.ChooseDecision(testContext, r.DecisionID, "Try again")
	if currentRelease(t, s, p.ID).State != "approved" {
		t.Fatal("retry not approved")
	}
	s.ReleaseFailed(testContext, p.ID, "diverged", true)
	r = currentRelease(t, s, p.ID)
	s.ChooseDecision(testContext, r.DecisionID, "Leave it")
	snap, _ := s.Snapshot(testContext)
	p, _ = findProjectByID(snap, p.ID)
	if p.Release != nil || len(p.Releases) != 1 || p.Releases[0].Published != "" {
		t.Fatal(p)
	}
}
func TestReleaseRefusalsPreservePMAnswer(t *testing.T) {
	for _, mode := range []string{"no settings", "nothing new", "old version", "tags unavailable"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t)
			p := releaseProject(t, s, ApprovePM)
			in := proposal()
			in.Note = "still ordered"
			switch mode {
			case "no settings":
				s.store.update(testContext, func(v *Snapshot) error { project(v, p.ID).Playbook.Release = nil; return nil })
			case "nothing new":
				in.ReleaseContext.Count = 0
			case "old version":
				in.Release.Version = "v1.0.0"
			case "tags unavailable":
				in.ReleaseContext.Error = "fetch failed"
			}
			if _, err := s.ApplyPM(testContext, p.ID, in); err != nil {
				t.Fatal(err)
			}
			if currentRelease(t, s, p.ID) != nil {
				t.Fatal("release created")
			}
			snap, _ := s.Snapshot(testContext)
			if !slices.ContainsFunc(snap.Activity, func(a Activity) bool { return strings.Contains(a.Summary, "still ordered") }) {
				t.Fatal("PM answer lost")
			}
		})
	}
}
func TestReleaseClaimHoldsAndPins(t *testing.T) {
	s, _ := fixture(t)
	p := releaseProject(t, s, ApprovePM)
	s.ApplyPM(testContext, p.ID, proposal())
	s.store.update(testContext, func(v *Snapshot) error { project(v, p.ID).LandingPaused = &LandingPause{}; return nil })
	for range 3 {
		if _, _, ok, err := s.ClaimRelease(testContext, p.ID, "abc", anyone); err != nil || ok {
			t.Fatal(ok, err)
		}
	}
	s.store.update(testContext, func(v *Snapshot) error { project(v, p.ID).LandingPaused = nil; return nil })
	c, _, ok, err := s.ClaimRelease(testContext, p.ID, "abc", anyone)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, _, ok, _ := s.ClaimRelease(testContext, p.ID, "def", anyone); ok {
		t.Fatal("second claim")
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	c, _, ok, err = s.ClaimRelease(testContext, p.ID, "def", anyone)
	if err != nil || !ok || currentRelease(t, s, p.ID).Commit != "abc" {
		t.Fatal("not pinned", err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
}

func TestReleaseWaitsForLandingAndDelivering(t *testing.T) {
	for _, status := range []string{TaskLanding, TaskDelivered} {
		t.Run(status, func(t *testing.T) {
			s, _ := fixture(t)
			p := releaseProject(t, s, ApprovePM)
			s.ApplyPM(testContext, p.ID, proposal())
			s.store.update(testContext, func(v *Snapshot) error {
				t := Task{ID: "landing", ProjectID: p.ID, Status: status}
				if status == TaskDelivered {
					t.Delivering = &Delivering{}
				}
				v.Tasks = append(v.Tasks, t)
				return nil
			})
			for range 3 {
				if _, _, ok, err := s.ClaimRelease(testContext, p.ID, "abc", anyone); err != nil || ok {
					t.Fatal(ok, err)
				}
			}
			s.store.update(testContext, func(v *Snapshot) error { v.Tasks = nil; return nil })
			c, _, ok, err := s.ClaimRelease(testContext, p.ID, "abc", anyone)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			s.ReleaseProjectClaim(testContext, p.ID, c.Token)
		})
	}
}

func TestReleaseFinishHonoursTheLandingGate(t *testing.T) {
	s, _ := fixture(t)
	p := releaseProject(t, s, ApprovePM)
	s.ApplyPM(testContext, p.ID, proposal())
	c, _, ok, err := s.ClaimRelease(testContext, p.ID, "abc", anyone)
	if err != nil || !ok {
		t.Fatal(err)
	}
	fenced := FencedProject(testContext, p.ID, c.Token)
	s.SetLandingPaused(testContext, p.ID, true, "freeze")
	if err := s.FinishRelease(fenced, p.ID, "", "tag not published"); err == nil {
		t.Fatal("recorded during freeze")
	}
	if currentRelease(t, s, p.ID) == nil {
		t.Fatal("lost publishing intent")
	}
	s.SetLandingPaused(testContext, p.ID, false, "")
	if err := s.FinishRelease(fenced, p.ID, "", "tag not published"); err != nil {
		t.Fatal(err)
	}
	s.ReleaseProjectClaim(testContext, p.ID, c.Token)
	if currentRelease(t, s, p.ID) != nil {
		t.Fatal("did not record")
	}
}

func TestReleaseClosingDispositionsAllowAnotherProposal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		for _, local := range []bool{false, true} {
			for _, custom := range []bool{false, true} {
				t.Run(fmt.Sprintf("failed=%t/local=%t/custom=%t", failed, local, custom), func(t *testing.T) {
					s, _ := fixture(t)
					p := releaseProject(t, s, "")
					in := proposal()
					s.ApplyPM(testContext, p.ID, in)
					r := currentRelease(t, s, p.ID)
					if failed {
						s.ChooseDecision(testContext, r.DecisionID, "Release v1.1.0")
						s.ReleaseFailed(testContext, p.ID, "failed", local)
						r = currentRelease(t, s, p.ID)
					}
					var err error
					if custom {
						_, err = s.AnswerDecision(testContext, r.DecisionID, "Try again")
					} else {
						_, err = s.DismissDecision(testContext, r.DecisionID, "obsolete")
					}
					if err != nil {
						t.Fatal(err)
					}
					if currentRelease(t, s, p.ID) != nil {
						t.Fatal("closed decision kept pending release")
					}
					snap, _ := s.Snapshot(testContext)
					saved, _ := findProjectByID(snap, p.ID)
					if (len(saved.Releases) > 0) != (failed && local) {
						t.Fatal("wrong local release record", saved.Releases)
					}
					if _, err = s.ApplyPM(testContext, p.ID, in); err != nil {
						t.Fatal(err)
					}
					if currentRelease(t, s, p.ID) == nil {
						t.Fatal("later proposal refused")
					}
				})
			}
		}
	}
}
func TestReleaseRefusesOccupiedOffTargetVersion(t *testing.T) {
	s, _ := fixture(t)
	p := releaseProject(t, s, "")
	in := proposal()
	in.ReleaseContext.Highest = "v1.2.0"
	s.ApplyPM(testContext, p.ID, in)
	if currentRelease(t, s, p.ID) != nil {
		t.Fatal("occupied version accepted")
	}
}

func TestReleaseValidatesSemverBeforeOccupiedVersion(t *testing.T) {
	for _, version := range []string{"next", "v1", "v01.2.0", "v1.2.0-01"} {
		t.Run(version, func(t *testing.T) {
			s, _ := fixture(t)
			p := releaseProject(t, s, "")
			in := proposal()
			in.Release.Version = version
			in.ReleaseContext.Highest = "v2.0.0"
			snap, _ := s.Snapshot(testContext)
			err := proposeRelease(&snap, &p, *in.Release, in.ReleaseContext, s.now())
			if err == nil || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "strict semver") {
				t.Fatal("wrong refusal", err)
			}
			if _, err = s.ApplyPM(testContext, p.ID, in); err != nil {
				t.Fatal(err)
			}
			snap, _ = s.Snapshot(testContext)
			if !slices.ContainsFunc(snap.Activity, func(a Activity) bool {
				return a.Kind == "release.refused" && strings.Contains(a.Summary, "strict semver") && !strings.Contains(a.Summary, "occupied")
			}) {
				t.Fatal("activity did not explain invalid version", snap.Activity)
			}
		})
	}
}
