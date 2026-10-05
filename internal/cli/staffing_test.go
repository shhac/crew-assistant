package cli

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/app"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/server"
	libcli "github.com/shhac/lib-agent-cli/cli"
	output "github.com/shhac/lib-agent-output"
	"github.com/spf13/cobra"
)

// The repository, team and project commands do what the dashboard's
// settings do, against the daemon.
func TestRepositoryTeamAndProjectCommands(t *testing.T) {
	o := standIn(t, nil, "unused")
	o.globals = &libcli.Globals{Format: string(output.FormatNDJSON)}
	store, err := core.Open(o.statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := config.Default()
	a := app.New(core.NewService(store, cfg), cfg, filepath.Join(filepath.Dir(o.statePath), "config.json"), app.Options{})
	auth, err := server.NewAuth(o.runtimeDir(), "http://daemon.invalid", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.New(a, auth)
	o.transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:12345"
		handler.ServeHTTP(w, r)
	})}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Core.CreateProject(t.Context(), core.ProjectInput{Title: "Web", Directories: []string{dir}, Brief: core.BriefInput{Goal: "Ship", Criteria: []string{"Works"}}})
	if err != nil {
		t.Fatal(err)
	}
	ada, err := a.Core.SaveMember(t.Context(), "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleReviewer}, Engine: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(cmd *cobra.Command, args ...string) {
		t.Helper()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run(repositoryCommand(o), "add", "--path", dir, "--name", "Mono", "--check", "make check", "--prepare", "**/node_modules", "--area", "web=web/**,shared")
	run(repositoryCommand(o), "set", "Mono", "--check-loopback", "--run-start", "npm start", "--run-url", "http://127.0.0.1:{port}/")
	run(teamCommand(o), "add", "Core")
	run(teamCommand(o), "set", "core", "--max-rounds", "5")
	run(projectCommand(o), "setup", "Web", "--team", "Core", "--repository", "Mono", "--area", "web")
	run(projectCommand(o), "overrides", "Web", "--exclude", "researcher", "--add", "reviewer=Ada")
	snap, err := a.Core.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, team, project := snap.Repositories[0], snap.Teams[0], snap.Projects[0]
	if r.Name != "Mono" || r.Check != "make check" || !r.CheckLoopback || r.Run == nil || r.Run.Start != "npm start" || len(r.Areas) != 1 || len(r.Areas[0].Paths) != 2 {
		t.Fatalf("repository %+v", r)
	}
	if team.Name != "Core" || team.MaxRounds != 5 {
		t.Fatalf("team %+v", team)
	}
	if project.ID != p.ID || project.Team != team.ID || project.Scope.Repositories[0].ID != r.ID || project.Scope.Repositories[0].Areas[0] != "web" {
		t.Fatalf("project %+v", project)
	}
	pb := project.Playbook
	if pb.Repo != dir || !pb.CheckLoopback || pb.MaxRounds != 5 || pb.Roles[0].Name == "Researcher" || pb.Roles[len(pb.Roles)-2].Member != ada.ID {
		t.Fatalf("playbook %+v", pb)
	}
	// Ada can join the team once Web no longer adds her seat itself.
	run(projectCommand(o), "overrides", "Web", "--clear")
	run(teamCommand(o), "seat", "Core", "reviewer", "Ada")
	snap, _ = a.Core.Snapshot(t.Context())
	if len(snap.Projects[0].SeatOverrides) != 0 || snap.Teams[0].Roles[2].Member != ada.ID {
		t.Fatalf("after seating Ada on the team: %+v", snap.Teams[0].Roles)
	}
}
