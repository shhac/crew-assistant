package core

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Older playbooks, as schema 3 stored them on each project.
const (
	codePlaybookV3 = `{"disabled_bundled_skills":["sprite-atlas"],"template":"code","medium":"git","roles":[{"name":"Ada","kinds":["researcher","implementer"],"engine":"claude","model":"opus","member":"m-ada","instructions":"Small commits."},{"name":"Reviewer","kinds":["reviewer"],"engine":"codex"},{"name":"QA","kinds":["qa"],"engine":"claude","browser":{"on":true,"name":"Chrome"}},{"name":"Pim","kinds":["pm"],"engine":"claude","member":"m-pim"}],"max_rounds":4,"deliver":"owner","repo":"/work/app","branch_prefix":"team/","check":"make check","prepare":["node_modules","**/vendor"],"check_in_copy":true,"check_loopback":true,"run":{"setup":"npm ci","start":"npm start","url":"http://127.0.0.1:{port}/"},"sign":"always","land":{"via":"push","target":"main","method":"fast-forward","approve":"pm"},"release":{"when":"pm","check":"make release-check"},"max_active":2,"stage_limits":{"reviewing":3}}`
	unsetCodeV3    = `{"template":"code","medium":"git","roles":[{"name":"Implementer","kinds":["implementer"],"engine":"claude"},{"name":"Reviewer","kinds":["reviewer"],"engine":"codex"}],"max_rounds":3,"deliver":"owner","branch_prefix":"crew/"}`
	draftV3        = `{"template":"draft","medium":"documents","roles":[{"name":"Writer","kinds":["implementer"],"engine":"claude"},{"name":"Reviewer","kinds":["reviewer"],"engine":"codex"}],"max_rounds":2,"deliver":"owner","deliver_to":"/work/out","stage_limits":{"implementing":1}}`
	// A monorepo landing by draft pull requests, trusting a review bot, and
	// copying in dependencies by pattern.
	draftPRsV3 = `{"template":"code","medium":"git","roles":[{"name":"Implementer","kinds":["implementer"],"engine":"claude"},{"name":"Reviewer","kinds":["reviewer"],"engine":"codex"},{"name":"QA","kinds":["qa"],"engine":"codex"}],"max_rounds":3,"deliver":"owner","repo":"/work/mono","branch_prefix":"crew/","check":"make check","prepare":["**/node_modules","packages/*/dist"],"land":{"target":"main","pull_requests":true,"github":"o/mono","merge":"squash","open":"pm","approve":"pm","draft":true,"trusted_bots":["review-bot[bot]","ci-helper"]}}`
	legacyV3   = `{"template":"code","medium":"git","roles":[{"name":"Planner","kind":"planner","engine":"claude"},{"name":"Implementer","kind":"implementer","engine":"claude"},{"name":"Reviewer","kinds":["reviewer"],"engine":"codex"}],"max_rounds":3,"deliver":"owner","repo":"/work/legacy","branch_prefix":"crew/"}`
)

func schema3State() string {
	project := func(id, title, playbook string) string {
		p := `{"id":"` + id + `","title":"` + title + `","prefix":"` + strings.ToUpper(id) + `","status":"active","brief":{"criteria":[]},"directories":[],"operator_permission":{"allowed":true,"revision":3,"by":"owner"}`
		if playbook != "" {
			p += `,"playbook":` + playbook
		}
		return p + `}`
	}
	projects := strings.Join([]string{project("pa", "App", codePlaybookV3), project("pb", "Unset", unsetCodeV3), project("pc", "Memo", draftV3), project("pd", "Bare", ""), project("pe", "Legacy", legacyV3), project("pf", "Mono", draftPRsV3)}, ",")
	task := `{"id":"t1","project_id":"pa","number":1,"objective":"Pinned","status":"waiting","round":1,"max_rounds":4,"playbook":` + codePlaybookV3 + `,"roles":[]}`
	return `{"schema":3,"snapshot":{"projects":[` + projects + `],"tasks":[` + task + `],"decisions":[],"messages":[],"memories":[],"activity":[],"integrations":[],"members":[]},"events":{},"model_calls":{}}`
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodedPlaybook(t *testing.T, raw string) Playbook {
	t.Helper()
	var pb Playbook
	if err := json.Unmarshal([]byte(raw), &pb); err != nil {
		t.Fatal(err)
	}
	return pb
}

func openState(t *testing.T, path string) *Service {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, _ := fixture(t)
	s.store = st
	return s
}

// Each code project becomes one repository and one team of its own, the
// same project naming both, and every playbook reads back exactly as it was,
// through a restart.
func TestSchema3ProjectsBecomeRepositoriesAndTeamsWithTheSamePlaybooks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writeRawState(t, path, schema3State())
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := st.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	byID := func(s Snapshot, id string) Project { return *project(&s, id) }
	for id, raw := range map[string]string{"pa": codePlaybookV3, "pb": unsetCodeV3, "pc": draftV3, "pf": draftPRsV3} {
		if got, want := asJSON(t, byID(snap, id).Playbook), asJSON(t, decodedPlaybook(t, raw)); got != want {
			t.Fatalf("%s's playbook changed:\n got %s\nwant %s", id, got, want)
		}
	}
	app := byID(snap, "pa")
	repo, team := snap.Repository("repo-pa"), snap.Team("team-pa")
	if repo == nil || team == nil || app.Team != team.ID || firstRepository(app) != repo.ID {
		t.Fatalf("app is not linked to its own repository and team: %+v %+v %+v", app.Scope, repo, team)
	}
	if repo.Name != "app" || repo.Path != "/work/app" || repo.Check != "make check" || !slices.Equal(repo.Prepare, []string{"node_modules", "**/vendor"}) || !repo.CheckInCopy || !repo.CheckLoopback || repo.Run == nil || repo.Run.Setup != "npm ci" {
		t.Fatalf("repository %+v", repo)
	}
	if team.Name != "App" || team.Template != "code" || team.MaxRounds != 4 || len(team.Roles) != 4 || team.Roles[3].Member != "m-pim" || !team.Roles[3].Holds(RolePM) {
		t.Fatalf("team %+v", team)
	}
	// What stays with the project: landing and release, limits, skills.
	own := app.Settings
	if own.Repo != "" || own.Check != "" || own.Roles != nil || own.MaxRounds != 0 || own.Run != nil || own.Land.Approve != ApprovePM || own.Release == nil || own.BranchPrefix != "team/" || own.Sign != SignAlways || own.MaxActive != 2 || !slices.Equal(own.DisabledBundledSkills, []string{"sprite-atlas"}) {
		t.Fatalf("settings %+v", own)
	}
	if !app.OperatorPermission.Allowed || app.OperatorPermission.Revision != 3 {
		t.Fatalf("operator permission %+v", app.OperatorPermission)
	}
	// Draft pull requests and trusted bots stay with the project's landing;
	// the prepare patterns go to the repository and still check.
	mono := byID(snap, "pf")
	if land := mono.Settings.Land; !land.Draft || !slices.Equal(land.TrustedBots, []string{"review-bot[bot]", "ci-helper"}) || !land.TrustsBot("review-bot") {
		t.Fatalf("mono's landing %+v", land)
	}
	if r := snap.Repository("repo-pf"); r == nil || !slices.Equal(r.Prepare, []string{"**/node_modules", "packages/*/dist"}) || r.validate() != nil || mono.Settings.Prepare != nil {
		t.Fatalf("mono's repository %+v", r)
	}
	if err := mono.Playbook.Validate(); err != nil {
		t.Fatalf("mono's playbook no longer checks: %v", err)
	}
	// A code project with no repository yet gets its team only.
	if unset := byID(snap, "pb"); unset.Team != "team-pb" || len(unset.Scope.Repositories) != 0 || snap.Repository("repo-pb") != nil {
		t.Fatalf("unset %+v", unset)
	}
	// Other work, and a project with no playbook, stand alone as before.
	if memo := byID(snap, "pc"); memo.Team != "" || len(memo.Scope.Repositories) != 0 || len(memo.Settings.Roles) != 2 {
		t.Fatalf("memo %+v", memo)
	}
	if bare := byID(snap, "pd"); bare.Settings != nil || bare.Playbook != nil || bare.Team != "" {
		t.Fatalf("bare %+v", bare)
	}
	// Seats older still are read as they always were, now on their team.
	if legacy := byID(snap, "pe"); legacy.Playbook.Roles[0].Name != "Researcher" || !slices.Equal(legacy.Playbook.Roles[0].Kinds, []string{RoleResearcher}) || !slices.Equal(legacy.Playbook.Roles[1].Kinds, []string{RoleImplementer}) {
		t.Fatalf("legacy seats %+v", legacy.Playbook.Roles)
	}
	// A task keeps the playbook it pinned.
	if got, want := asJSON(t, snap.Tasks[0].Playbook), asJSON(t, decodedPlaybook(t, codePlaybookV3)); got != want {
		t.Fatalf("pinned playbook changed:\n got %s\nwant %s", got, want)
	}
	st.Close()

	s := openState(t, path)
	again, err := s.Snapshot(testContext)
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, again.Projects) != asJSON(t, snap.Projects) || asJSON(t, again.Repositories) != asJSON(t, snap.Repositories) || asJSON(t, again.Teams) != asJSON(t, snap.Teams) {
		t.Fatal("a restart changed the projects, repositories or teams")
	}
	// A change after the upgrade goes to the owner and survives a restart.
	if _, err := s.EditPlaybook(testContext, "pa", func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.Check, pb.Land.Target = "make test", "release"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s = openState(t, path)
	after, _ := s.Snapshot(testContext)
	// A pattern that can't be matched is refused on the repository.
	if _, err := s.EditPlaybook(testContext, "pf", func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.Prepare = []string{"packages/[/dist"}
		return nil
	}); err == nil || !strings.Contains(err.Error(), "can't be matched") {
		t.Fatalf("an unmatchable prepare pattern: %v", err)
	}
	if r := after.Repository("repo-pa"); r.Check != "make test" || byID(after, "pa").Playbook.Check != "make test" || byID(after, "pa").Settings.Land.Target != "release" || byID(after, "pa").Settings.Check != "" {
		t.Fatalf("edit after upgrade: %+v %+v", r, byID(after, "pa").Settings)
	}
}

// codeDir is a folder a code project and its repository can share.
func codeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// staffedProject is a project, with dir among its folders, staffed by the
// team and working in the repository.
func staffedProject(t *testing.T, s *Service, title, dir, team, repo string, areas ...string) Project {
	t.Helper()
	p, err := s.CreateProject(testContext, ProjectInput{Title: title, Directories: []string{dir}, Brief: BriefInput{Goal: "A fictional outcome", Criteria: []string{"Demonstrated"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = s.SetProjectSetup(testContext, p.ID, ProjectSetup{Team: team, Repository: repo, Areas: areas}); err != nil {
		t.Fatal(err)
	}
	return p
}

// sharedSetup is a repository and a team two projects share: Ada implements
// for the team, and the repository's QA runs make check.
func sharedSetup(t *testing.T) (*Service, Repository, Team, Project, Project, Member) {
	t.Helper()
	s, _ := fixture(t)
	dir := codeDir(t)
	ada, err := s.SaveMember(testContext, "", MemberInput{Name: "Ada", Kinds: []string{RoleImplementer, RoleReviewer}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepository(testContext, RepositoryInput{Name: "Monorepo", Path: dir, Check: "make check", Prepare: []string{"**/node_modules"}, Areas: []CodeArea{{Name: "web", Paths: []string{"web/**"}}, {Name: "api", Paths: []string{"api"}}}})
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(testContext, TeamInput{Name: "Core", Template: "code"})
	if err != nil {
		t.Fatal(err)
	}
	team, err = s.EditTeam(testContext, team.ID, func(_ *Snapshot, _ *Team, pb *Playbook) error {
		pb.Roles = []Role{
			{Name: "Ada", Kinds: []string{RoleImplementer}, Engine: "claude", Member: ada.ID},
			{Name: "Reviewer", Kinds: []string{RoleReviewer}, Engine: "codex"},
			{Name: "QA", Kinds: []string{RoleQA}, Engine: "codex"},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	one := staffedProject(t, s, "Web", dir, team.ID, repo.ID, "web")
	two := staffedProject(t, s, "API", dir, team.ID, repo.ID, "api")
	return s, repo, team, one, two, ada
}

func TestProjectsShareARepositoryAndATeam(t *testing.T) {
	s, repo, team, one, two, _ := sharedSetup(t)
	for _, p := range []Project{one, two} {
		pb := p.Playbook
		if pb.Medium != MediumGit || pb.Repo != repo.Path || pb.Check != "make check" || !slices.Equal(pb.Prepare, []string{"**/node_modules"}) || len(pb.Roles) != 3 || pb.Roles[0].Name != "Ada" || pb.MaxRounds != team.MaxRounds {
			t.Fatalf("%s's playbook %+v", p.Title, pb)
		}
	}
	if !slices.Equal(one.Scope.Repositories[0].Areas, []string{"web"}) {
		t.Fatalf("scope %+v", one.Scope)
	}
	// The check set from one project is the repository's, so both run it.
	if _, err := s.EditPlaybook(testContext, one.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.Check = "make test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if project(&snap, two.ID).Playbook.Check != "make test" || snap.Repository(repo.ID).Check != "make test" {
		t.Fatal("the shared repository's check didn't reach the other project")
	}
	// Landing stays the project's own.
	if _, err := s.EditPlaybook(testContext, one.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.Land = LandPolicy{Via: LandPush, Target: "main", Method: "fast-forward"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if !project(&snap, two.ID).Playbook.Land.Unset() {
		t.Fatal("one project's landing reached the other")
	}
	// A shared change that would leave a project unusable is refused, and
	// says which.
	in := RepositoryInput{Name: repo.Name, Path: repo.Path, Areas: repo.Areas}
	if _, err := s.UpdateRepository(testContext, repo.ID, in); err == nil || !strings.Contains(err.Error(), "runs the check") {
		t.Fatalf("a repository without a check, with QA on the team: %v", err)
	}
	// So is dropping an area a project names.
	in.Check, in.Areas = "make test", repo.Areas[:1]
	if _, err := s.UpdateRepository(testContext, repo.ID, in); err == nil || !strings.Contains(err.Error(), "API's scope") {
		t.Fatalf("dropping a named area: %v", err)
	}
	if err := s.DeleteTeam(testContext, team.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a team in use: %v", err)
	}
	if err := s.DeleteRepository(testContext, repo.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleting a repository in use: %v", err)
	}
}

func TestSeatOverridesAdjustOneProjectsTeam(t *testing.T) {
	s, _, _, one, two, ada := sharedSetup(t)
	quinn, _ := s.SaveMember(testContext, "", MemberInput{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "claude"})
	rex := Role{Name: "Rex", Kinds: []string{RoleReviewer}, Engine: "codex", Instructions: "Security first."}
	overrides := []SeatOverride{
		{Action: OverrideReplace, Kind: RoleQA, Seat: &Role{Name: "Quinn", Kinds: []string{RoleQA}, Engine: "claude", Member: quinn.ID}},
		{Action: OverrideAdd, Kind: RoleReviewer, Seat: &rex},
		{Action: OverrideAdd, Kind: RoleDesigner, Seat: &Role{Name: "Dee", Kinds: []string{RoleDesigner}, Engine: "claude"}},
	}
	p, err := s.SetSeatOverrides(testContext, two.ID, overrides)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range p.Playbook.Roles {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{"Ada", "Reviewer", "Rex", "Quinn", "Dee"}) {
		t.Fatalf("effective seats %v", names)
	}
	snap, _ := s.Snapshot(testContext)
	if len(project(&snap, one.ID).Playbook.Roles) != 3 || len(snap.Team(one.Team).Roles) != 3 {
		t.Fatal("another project's override reached the team")
	}
	// Excluding the only reviewer leaves no review, which is refused.
	if _, err := s.SetSeatOverrides(testContext, one.ID, []SeatOverride{{Action: OverrideExclude, Kind: RoleReviewer}}); err == nil {
		t.Fatal("a project was left without a reviewer")
	}
	if _, err := s.SetSeatOverrides(testContext, one.ID, []SeatOverride{{Action: OverrideExclude, Kind: RoleQA}}); err != nil {
		t.Fatal(err)
	}
	// A member who leaves goes from the team, and from every override: a
	// kind it took over goes back to the template's seat.
	if err := s.DeleteMember(testContext, quinn.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	got := project(&snap, two.ID).SeatOverrides[0]
	if got.Action != OverrideReplace || got.Seat == nil || got.Seat.Member != "" || got.Seat.Name != "QA" {
		t.Fatalf("override after Quinn left %+v", got)
	}
	if err := s.DeleteMember(testContext, ada.ID); err != nil {
		t.Fatal(err)
	}
	snap, _ = s.Snapshot(testContext)
	if team := snap.Team(two.Team); team.Roles[0].Member != "" || team.Roles[0].Name != "Implementer" {
		t.Fatalf("team after Ada left %+v", team.Roles)
	}
}

// A member is one person: busy in one project, they are busy in every
// project their team staffs.
func TestASharedMemberBusyInOneProjectIsBusyInAll(t *testing.T) {
	s, _, _, one, two, _ := sharedSetup(t)
	queueAll(t, s, one, "Web change")
	queueAll(t, s, two, "API change")
	if got := claimed(t, s); !slices.Equal(got, []string{"Web change: writing by Ada"}) {
		t.Fatalf("first look: %v", got)
	}
	snap, _ := s.Snapshot(testContext)
	waiting := snap.Tasks[1]
	if waiting.Waiting == nil || waiting.Waiting.Member == "" || waiting.Waiting.Project != "Web" {
		t.Fatalf("the API change should wait for Ada, busy on Web: %+v", waiting.Waiting)
	}
}

// Leaving a team or a repository keeps what they held as the project's own,
// so it works on exactly as it did.
func TestAProjectLeavingItsTeamAndRepositoryWorksOnAsBefore(t *testing.T) {
	s, _, _, one, _, _ := sharedSetup(t)
	p, err := s.SetProjectSetup(testContext, one.ID, ProjectSetup{})
	if err != nil {
		t.Fatal(err)
	}
	if asJSON(t, p.Playbook) != asJSON(t, one.Playbook) || p.Team != "" || len(p.Scope.Repositories) != 0 || p.Settings.Check != "make check" || len(p.Settings.Roles) != 3 {
		t.Fatalf("stand-alone %+v", p.Settings)
	}
	// Taking the seats on as a team of their own.
	team, err := s.CreateTeam(testContext, TeamInput{Name: "Web team", FromProject: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if q := project(&snap, p.ID); q.Team != team.ID || q.Settings.Roles != nil || asJSON(t, q.Playbook) != asJSON(t, one.Playbook) {
		t.Fatalf("staffed by its seats as a team %+v", q)
	}
}

// A repository is for code; work that isn't code stands alone, as before.
func TestWorkThatIsNotCodeNeedsNoRepository(t *testing.T) {
	s, _ := fixture(t)
	dir := codeDir(t)
	repo, err := s.CreateRepository(testContext, RepositoryInput{Path: dir, Check: "make check"})
	if err != nil {
		t.Fatal(err)
	}
	memo, err := s.CreateProject(testContext, ProjectInput{Title: "Memo", Template: "draft", Directories: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProjectSetup(testContext, memo.ID, ProjectSetup{Repository: repo.ID}); err == nil || !strings.Contains(err.Error(), "code projects") {
		t.Fatalf("a repository for a memo: %v", err)
	}
	// A draft team can staff it, but a code team can't.
	draft, _ := s.CreateTeam(testContext, TeamInput{Name: "Writers", Template: "draft"})
	if _, err := s.SetProjectSetup(testContext, memo.ID, ProjectSetup{Team: draft.ID}); err != nil {
		t.Fatal(err)
	}
	code, _ := s.CreateTeam(testContext, TeamInput{Name: "Coders", Template: "code"})
	if _, err := s.SetProjectSetup(testContext, memo.ID, ProjectSetup{Team: code.ID}); err == nil {
		t.Fatal("a code team staffed a memo")
	}
	// A repository must be one of the project's folders.
	other, _ := s.CreateProject(testContext, ProjectInput{Title: "Elsewhere", Template: "code"})
	if _, err := s.SetProjectSetup(testContext, other.ID, ProjectSetup{Repository: repo.ID}); err == nil || !strings.Contains(err.Error(), "as a folder") {
		t.Fatalf("a repository outside the project's folders: %v", err)
	}
}

// A project's own settings stay its own when it shares a team.
func TestSharedTeamsKeepEachProjectsSkillsAndLimits(t *testing.T) {
	s, _, _, one, two, _ := sharedSetup(t)
	if _, err := s.SetBundledSkill(testContext, one.ID, "sprite-atlas", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditPlaybook(testContext, one.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.MaxActive = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if pb := project(&snap, two.ID).Playbook; len(pb.DisabledBundledSkills) != 0 || pb.MaxActive != 0 {
		t.Fatalf("one project's settings reached the other: %+v", pb)
	}
}

// A new folder for a shared repository gives the project a repository of
// its own, rather than moving the one others work in.
func TestANewPathForASharedRepositoryGivesTheProjectItsOwn(t *testing.T) {
	s, repo, _, one, two, _ := sharedSetup(t)
	elsewhere := codeDir(t)
	if _, err := s.SetProjectDirectories(testContext, one.ID, []string{repo.Path, elsewhere}); err != nil {
		t.Fatal(err)
	}
	p, err := s.EditPlaybook(testContext, one.ID, func(_ *Snapshot, _ *Project, pb *Playbook) error {
		pb.Repo = elsewhere
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	if firstRepository(p) == repo.ID || p.Playbook.Repo != elsewhere || p.Playbook.Check != "make check" || project(&snap, two.ID).Playbook.Repo != repo.Path || len(snap.Repositories) != 2 {
		t.Fatalf("moved %+v; repositories %+v", p.Scope, snap.Repositories)
	}
}

// Writing a project's derived playbook instead of its owner is refused, so
// no change is silently lost.
func TestADerivedPlaybookCantBeWrittenDirectly(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	err := s.store.update(testContext, func(v *Snapshot) error {
		project(v, p.ID).Playbook.MaxRounds = 9
		return nil
	})
	if !errors.Is(err, errPlaybookEdited) {
		t.Fatalf("a direct write was let through: %v", err)
	}
}
