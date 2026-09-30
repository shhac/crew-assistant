package work

import (
	"slices"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

func roleCrew() core.Snapshot {
	return core.Snapshot{Members: []core.Member{
		{ID: "ada", Name: "Ada", Kinds: []string{core.RoleImplementer, core.RoleResearcher}, Engine: "claude"},
		{ID: "sol", Name: "Sol", Kinds: []string{core.RoleImplementer, core.RoleReviewer}, Engine: "codex"},
		{ID: "pia", Name: "Pia", Kinds: []string{core.RolePM}, Engine: "claude"},
		{ID: "rue", Name: "Rue", Kinds: []string{core.RoleReviewer}, Engine: "codex"},
	}}
}

func codePlaybook() core.Playbook {
	pb := core.Templates["code"]
	pb.Roles = slices.Clone(pb.Roles)
	pb.Repo, pb.BranchPrefix, pb.Check = "/work/repo", "crew/", "make check"
	return pb
}

func seatsHolding(pb core.Playbook, kind string) []string {
	var out []string
	for _, r := range pb.Roles {
		if r.Holds(kind) {
			out = append(out, r.Name+"="+r.Member)
		}
	}
	return out
}

// People are added to a role one at a time: someone new gets a seat after
// the role's last, and someone already in it a seat alike beside theirs, to
// run a second step at once.
func TestSomeoneAddedToARoleGetsASeatInIt(t *testing.T) {
	pb := codePlaybook()
	snap := roleCrew()
	for _, add := range []string{"ada", "sol", "ada", ""} {
		if err := addToRole(&pb, core.RoleImplementer, add, snap); err != nil {
			t.Fatalf("adding %q: %v", add, err)
		}
	}
	got := seatsHolding(pb, core.RoleImplementer)
	want := []string{"Implementer=", "Implementer #2=", "Ada=ada", "Ada #2=ada", "Sol=sol"}
	if !slices.Equal(got, want) {
		t.Fatalf("implementers %v, want %v", got, want)
	}
	if err := pb.Validate(); err != nil {
		t.Fatalf("the team isn't valid: %v", err)
	}
}

// Someone already on the team in another role takes this one in the same
// seat, as one person, unless both roles do the work: a seat implements,
// reviews or runs QA, never two of them.
func TestSomeoneOnTheTeamTakesAnotherRoleInTheirSeatWhereTheyCan(t *testing.T) {
	pb := codePlaybook()
	snap := roleCrew()
	if err := addToRole(&pb, core.RoleImplementer, "ada", snap); err != nil {
		t.Fatal(err)
	}
	if err := addToRole(&pb, core.RoleResearcher, "ada", snap); err != nil {
		t.Fatal(err)
	}
	ada := pb.Roles[slices.IndexFunc(pb.Roles, func(r core.Role) bool { return r.Member == "ada" })]
	if !ada.Holds(core.RoleResearcher) || !ada.Holds(core.RoleImplementer) {
		t.Fatalf("Ada's seat %+v", ada)
	}
	if err := addToRole(&pb, core.RoleImplementer, "sol", snap); err != nil {
		t.Fatal(err)
	}
	if err := addToRole(&pb, core.RoleReviewer, "sol", snap); err != nil {
		t.Fatal(err)
	}
	if got := seatsHolding(pb, core.RoleReviewer); !slices.Equal(got, []string{"Reviewer=", "Sol #2=sol"}) {
		t.Fatalf("reviewers %v", got)
	}
	if err := pb.Validate(); err != nil {
		t.Fatalf("the team isn't valid: %v", err)
	}
}

func TestATeamKeepsOnePMAndOnlyMembersWhoHoldTheRole(t *testing.T) {
	pb := codePlaybook()
	snap := roleCrew()
	if err := addToRole(&pb, core.RolePM, "pia", snap); err != nil {
		t.Fatal(err)
	}
	if err := addToRole(&pb, core.RolePM, "pia", snap); err == nil || !strings.Contains(err.Error(), "one PM") {
		t.Fatalf("a second PM: %v", err)
	}
	if err := addToRole(&pb, core.RoleReviewer, "pia", snap); err == nil {
		t.Fatal("Pia doesn't review")
	}
	if err := addToRole(&pb, core.RoleDesigner, "", snap); err == nil {
		t.Fatal("no template fills the designer role")
	}
}

// Removing someone from a role takes their seat, or only that role where
// the seat holds others; the team keeps an implementer and a reviewer.
func TestSomeoneRemovedFromARoleKeepsTheirOtherRoles(t *testing.T) {
	pb := codePlaybook()
	snap := roleCrew()
	for _, kind := range []string{core.RoleImplementer, core.RoleResearcher} {
		if err := addToRole(&pb, kind, "ada", snap); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeFromRole(&pb, "ada", core.RoleResearcher); err != nil {
		t.Fatal(err)
	}
	if got := seatsHolding(pb, core.RoleImplementer); !slices.Equal(got, []string{"Implementer=", "Ada=ada"}) {
		t.Fatalf("implementers %v", got)
	}
	if got := seatsHolding(pb, core.RoleResearcher); !slices.Equal(got, []string{"Researcher="}) {
		t.Fatalf("researchers %v", got)
	}
	if err := removeFromRole(&pb, "Ada", core.RoleReviewer); err == nil {
		t.Fatal("Ada isn't a reviewer")
	}
	if err := removeFromRole(&pb, "Reviewer", core.RoleReviewer); err != nil {
		t.Fatal(err)
	}
	if err := pb.Validate(); err == nil {
		t.Fatal("a team without a reviewer was valid")
	}
}
