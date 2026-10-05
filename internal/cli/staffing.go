package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/work"
)

// registerStaffing is the owner's repositories and teams, and which of them
// each project works in and is staffed by, as the dashboard's settings have
// them.
func registerStaffing(root *cobra.Command, o *options) {
	root.AddCommand(repositoryCommand(o), teamCommand(o), projectCommand(o))
}

// used is which projects use each repository or team, by id.
func used(snap core.Snapshot, repository bool) map[string][]string {
	out := map[string][]string{}
	for _, p := range snap.Projects {
		if repository {
			for _, s := range p.Scope.Repositories {
				out[s.ID] = append(out[s.ID], p.Title)
			}
		} else if p.Team != "" {
			out[p.Team] = append(out[p.Team], p.Title)
		}
	}
	return out
}

// pick is the one of items whose id or name ref names: an exact id, a
// unique start of one, or a name in any case.
func pick[T any](items []T, ref, kind string, id, name func(T) string) (T, error) {
	var zero T
	ref = strings.TrimSpace(ref)
	var found []T
	for _, item := range items {
		switch {
		case id(item) == ref:
			return item, nil
		case strings.HasPrefix(id(item), ref) || strings.EqualFold(name(item), ref):
			found = append(found, item)
		}
	}
	switch len(found) {
	case 0:
		return zero, fmt.Errorf("no %s %q", kind, ref)
	case 1:
		return found[0], nil
	}
	return zero, fmt.Errorf("%q names more than one %s; use its id", ref, kind)
}

func (o *options) findRepository(ref string) (core.Repository, core.Snapshot, error) {
	snap, err := o.snapshot()
	if err != nil {
		return core.Repository{}, snap, err
	}
	r, err := pick(snap.Repositories, ref, "repository", func(r core.Repository) string { return r.ID }, func(r core.Repository) string { return r.Name })
	return r, snap, err
}

func (o *options) findTeam(ref string) (core.Team, core.Snapshot, error) {
	snap, err := o.snapshot()
	if err != nil {
		return core.Team{}, snap, err
	}
	t, err := pick(snap.Teams, ref, "team", func(t core.Team) string { return t.ID }, func(t core.Team) string { return t.Name })
	return t, snap, err
}

func (o *options) findProject(ref string) (core.Project, core.Snapshot, error) {
	snap, err := o.snapshot()
	if err != nil {
		return core.Project{}, snap, err
	}
	p, err := pick(snap.Projects, ref, "project", func(p core.Project) string { return p.ID }, func(p core.Project) string { return p.Title })
	return p, snap, err
}

// repositoryFlags are a repository's settings as flags; only those given
// change it.
type repositoryFlags struct {
	in                                   core.RepositoryInput
	areas                                []string
	runStart, runURL, runSetup, runReady string
	noRun                                bool
}

func (f *repositoryFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.in.Name, "name", "", "Name it is shown by")
	cmd.Flags().StringVar(&f.in.Path, "path", "", "Absolute path of the local clone")
	cmd.Flags().StringVar(&f.in.DefaultBranch, "default-branch", "", "The branch work starts from and lands on by default")
	cmd.Flags().StringVar(&f.in.Check, "check", "", "The full check QA and releases run")
	cmd.Flags().StringSliceVar(&f.in.Prepare, "prepare", nil, "Ignored paths or patterns, such as **/node_modules, copied into each private copy")
	cmd.Flags().BoolVar(&f.in.CheckInCopy, "check-in-copy", false, "Run the check in a writable copy of the revision")
	cmd.Flags().BoolVar(&f.in.CheckLoopback, "check-loopback", false, "Let the check bind and reach this machine's own addresses")
	cmd.Flags().StringArrayVar(&f.areas, "area", nil, "A code area, as name=path,path; repeat for more; replaces all areas")
	cmd.Flags().StringVar(&f.runStart, "run-start", "", "How QA starts the app (with --run-url)")
	cmd.Flags().StringVar(&f.runURL, "run-url", "", "Where the app answers, with {port}, such as http://127.0.0.1:{port}/")
	cmd.Flags().StringVar(&f.runSetup, "run-setup", "", "What runs before the app starts")
	cmd.Flags().StringVar(&f.runReady, "run-ready", "", "A command that succeeds once the app is ready")
	cmd.Flags().BoolVar(&f.noRun, "no-run", false, "Take the run recipe away, so QA only runs the check")
}

// over is the repository's settings with the flags given laid over them.
func (f *repositoryFlags) over(cmd *cobra.Command, r core.Repository) (core.RepositoryInput, error) {
	in := core.RepositoryInput{Name: r.Name, Path: r.Path, DefaultBranch: r.DefaultBranch, Check: r.Check, Prepare: r.Prepare, CheckInCopy: r.CheckInCopy, CheckLoopback: r.CheckLoopback, Run: r.Run, Areas: r.Areas}
	changed := cmd.Flags().Changed
	for flag, apply := range map[string]func(){
		"name": func() { in.Name = f.in.Name }, "path": func() { in.Path = f.in.Path }, "default-branch": func() { in.DefaultBranch = f.in.DefaultBranch },
		"check": func() { in.Check = f.in.Check }, "prepare": func() { in.Prepare = f.in.Prepare }, "check-in-copy": func() { in.CheckInCopy = f.in.CheckInCopy },
		"check-loopback": func() { in.CheckLoopback = f.in.CheckLoopback },
	} {
		if changed(flag) {
			apply()
		}
	}
	if changed("area") {
		in.Areas = nil
		for _, a := range f.areas {
			name, paths, ok := strings.Cut(a, "=")
			if !ok {
				return in, fmt.Errorf("an area is name=path,path, not %q", a)
			}
			in.Areas = append(in.Areas, core.CodeArea{Name: name, Paths: strings.Split(paths, ",")})
		}
	}
	switch {
	case f.noRun:
		in.Run = nil
	case changed("run-start") || changed("run-url") || changed("run-setup") || changed("run-ready"):
		run := core.RunRecipe{}
		if in.Run != nil {
			run = *in.Run
		}
		for _, field := range []struct {
			flag      string
			to, value *string
		}{{"run-start", &run.Start, &f.runStart}, {"run-url", &run.URL, &f.runURL}, {"run-setup", &run.Setup, &f.runSetup}, {"run-ready", &run.Ready, &f.runReady}} {
			if changed(field.flag) {
				*field.to = *field.value
			}
		}
		in.Run = &run
	}
	return in, nil
}

func repositoryCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{Use: "repository", Aliases: []string{"repo"}, Short: "Register the repositories projects work in: checks, preparation, run recipe and code areas"}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List repositories and the projects working in each", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		snap, err := o.snapshot()
		if err != nil {
			return err
		}
		users := used(snap, true)
		for _, r := range snap.Repositories {
			if err := o.emit(map[string]any{"id": r.ID, "name": r.Name, "path": r.Path, "check": r.Check, "projects": users[r.ID]}); err != nil {
				return err
			}
		}
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "show <repository>", Short: "Show a repository's settings", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, snap, err := o.findRepository(args[0])
		if err != nil {
			return err
		}
		return o.emit(struct {
			core.Repository
			Projects []string `json:"projects"`
		}{r, used(snap, true)[r.ID]})
	}})
	var added repositoryFlags
	add := &cobra.Command{Use: "add", Short: "Register a repository by its local clone's path", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		in, err := added.over(cmd, core.Repository{})
		if err != nil {
			return err
		}
		v, err := o.request("POST", "/api/repositories", in)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	added.add(add)
	var set repositoryFlags
	change := &cobra.Command{Use: "set <repository>", Short: "Change a repository for every project working in it", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := o.findRepository(args[0])
		if err != nil {
			return err
		}
		in, err := set.over(cmd, r)
		if err != nil {
			return err
		}
		v, err := o.request("PUT", "/api/repositories/"+url.PathEscape(r.ID), in)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	set.add(change)
	cmd.AddCommand(add, change, &cobra.Command{Use: "remove <repository>", Short: "Remove a repository no project works in", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r, _, err := o.findRepository(args[0])
		if err != nil {
			return err
		}
		v, err := o.request("DELETE", "/api/repositories/"+url.PathEscape(r.ID), nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	return cmd
}

func teamCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{Use: "team", Short: "Keep teams: default seats by kind of role, a PM, and rounds before the PM escalates"}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List teams and the projects each staffs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		snap, err := o.snapshot()
		if err != nil {
			return err
		}
		users := used(snap, false)
		for _, t := range snap.Teams {
			seats := make([]string, len(t.Roles))
			for i, r := range t.Roles {
				seats[i] = r.Name + " (" + strings.Join(r.Kinds, ", ") + ")"
			}
			if err := o.emit(map[string]any{"id": t.ID, "name": t.Name, "seats": seats, "max_rounds": t.MaxRounds, "projects": users[t.ID]}); err != nil {
				return err
			}
		}
		return nil
	}})
	cmd.AddCommand(&cobra.Command{Use: "show <team>", Short: "Show a team's seats", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, snap, err := o.findTeam(args[0])
		if err != nil {
			return err
		}
		return o.emit(struct {
			core.Team
			Projects []string `json:"projects"`
		}{t, used(snap, false)[t.ID]})
	}})
	var template, from string
	add := &cobra.Command{Use: "add <name>", Short: "Make a team from a template, or from a project's seats, which then becomes its team", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		in := core.TeamInput{Name: args[0], Template: template}
		if from != "" {
			p, _, err := o.findProject(from)
			if err != nil {
				return err
			}
			in.FromProject = p.ID
		}
		v, err := o.request("POST", "/api/teams", in)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	add.Flags().StringVar(&template, "template", "code", "The template its seats start from: code or draft")
	add.Flags().StringVar(&from, "from-project", "", "Take this project's seats as the team, by id or title")
	var name string
	var rounds int
	set := &cobra.Command{Use: "set <team>", Short: "Rename a team, or change its rounds before the PM escalates", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, _, err := o.findTeam(args[0])
		if err != nil {
			return err
		}
		in := core.TeamUpdate{Name: t.Name, MaxRounds: t.MaxRounds}
		if cmd.Flags().Changed("name") {
			in.Name = name
		}
		if cmd.Flags().Changed("max-rounds") {
			in.MaxRounds = rounds
		}
		v, err := o.request("PUT", "/api/teams/"+url.PathEscape(t.ID), in)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	set.Flags().StringVar(&name, "name", "", "New name")
	set.Flags().IntVar(&rounds, "max-rounds", 0, "Rounds before the PM must escalate, 1 to 10")
	seat := &cobra.Command{Use: "seat <team> <kind> [<member>]", Short: "Give a kind of role to a member, or with none back to the template's seat; \"none\" leaves research out", Args: cobra.RangeArgs(2, 3), RunE: func(cmd *cobra.Command, args []string) error {
		t, snap, err := o.findTeam(args[0])
		if err != nil {
			return err
		}
		member := ""
		if len(args) == 3 {
			if member, err = memberID(snap, args[2]); err != nil {
				return err
			}
		}
		v, err := o.request("PUT", "/api/teams/"+url.PathEscape(t.ID)+"/seats/"+url.PathEscape(args[1]), map[string]string{"member": member})
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	cmd.AddCommand(add, set, seat, &cobra.Command{Use: "remove <team>", Short: "Remove a team no project is staffed by", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		t, _, err := o.findTeam(args[0])
		if err != nil {
			return err
		}
		v, err := o.request("DELETE", "/api/teams/"+url.PathEscape(t.ID), nil)
		if err != nil {
			return err
		}
		return o.emit(v)
	}})
	return cmd
}

// memberID is the member ref names, by id or name; "none" passes through.
func memberID(snap core.Snapshot, ref string) (string, error) {
	if ref == "" || ref == work.NoResearcher {
		return ref, nil
	}
	m, err := pick(snap.Members, ref, "member", func(m core.Member) string { return m.ID }, func(m core.Member) string { return m.Name })
	return m.ID, err
}

func projectCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Choose a project's team, repository, code areas and seat overrides"}
	var team, repository string
	var areas []string
	setup := &cobra.Command{Use: "setup <project>", Short: "Staff a project with a team, or its own seats, and choose the repository and code areas it works in", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, snap, err := o.findProject(args[0])
		if err != nil {
			return err
		}
		in := core.ProjectSetup{Team: p.Team, Areas: areas}
		if len(p.Scope.Repositories) > 0 {
			in.Repository = p.Scope.Repositories[0].ID
			if !cmd.Flags().Changed("area") && !cmd.Flags().Changed("repository") {
				in.Areas = p.Scope.Repositories[0].Areas
			}
		}
		if cmd.Flags().Changed("team") {
			in.Team = ""
			if team != "" {
				t, err := pick(snap.Teams, team, "team", func(t core.Team) string { return t.ID }, func(t core.Team) string { return t.Name })
				if err != nil {
					return err
				}
				in.Team = t.ID
			}
		}
		if cmd.Flags().Changed("repository") {
			in.Repository = ""
			if repository != "" {
				r, err := pick(snap.Repositories, repository, "repository", func(r core.Repository) string { return r.ID }, func(r core.Repository) string { return r.Name })
				if err != nil {
					return err
				}
				in.Repository = r.ID
			}
		}
		v, err := o.request("PUT", "/api/projects/"+url.PathEscape(p.ID)+"/setup", in)
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	setup.Flags().StringVar(&team, "team", "", "The team, by id or name; empty for the project's own seats")
	setup.Flags().StringVar(&repository, "repository", "", "The repository, by id or name; empty for none")
	setup.Flags().StringSliceVar(&areas, "area", nil, "Code areas of the repository the project expects to touch")
	var add, replace, exclude []string
	overrides := &cobra.Command{Use: "overrides <project>", Short: "Set the project's seat overrides on its team: add, replace or exclude a kind of role, for this project only", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		p, snap, err := o.findProject(args[0])
		if err != nil {
			return err
		}
		// Overrides apply in order: what is left out, then what is taken
		// over, then what is added.
		var choices []work.SeatOverrideChoice
		for _, kind := range exclude {
			choices = append(choices, work.SeatOverrideChoice{Action: core.OverrideExclude, Kind: kind})
		}
		for _, given := range []struct {
			action string
			list   []string
		}{{core.OverrideReplace, replace}, {core.OverrideAdd, add}} {
			for _, item := range given.list {
				kind, member, _ := strings.Cut(item, "=")
				if member, err = memberID(snap, member); err != nil {
					return err
				}
				choices = append(choices, work.SeatOverrideChoice{Action: given.action, Kind: kind, Member: member})
			}
		}
		if len(choices) == 0 && !cmd.Flags().Changed("clear") {
			return errors.New("say what to add, replace or exclude, or --clear to take every override away")
		}
		v, err := o.request("PUT", "/api/projects/"+url.PathEscape(p.ID)+"/seat-overrides", map[string]any{"overrides": choices})
		if err != nil {
			return err
		}
		return o.emit(v)
	}}
	overrides.Flags().StringArrayVar(&exclude, "exclude", nil, "A kind of role to leave out, such as researcher")
	overrides.Flags().StringArrayVar(&replace, "replace", nil, "kind=member: the member holds that kind instead of the team's seats; no member for the template's seat")
	overrides.Flags().StringArrayVar(&add, "add", nil, "kind=member: one more seat, such as reviewer=Rex")
	overrides.Flags().Bool("clear", false, "Take every override away")
	cmd.AddCommand(setup, overrides)
	return cmd
}
