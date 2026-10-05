package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Repository is a codebase the team can work in, registered once and shared
// by every project that works in it. It holds what belongs to the code
// rather than to who works on it: the full check and how it runs, what a
// private copy needs copied in, how QA runs the app, and the code areas
// projects name in their scope.
type Repository struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Path is the owner's local clone, one of each using project's folders.
	Path string `json:"path"`
	// DefaultBranch is the branch the repository's work starts from and
	// lands on by default, when the owner has said; empty leaves it to the
	// landing settings, as before.
	DefaultBranch string `json:"default_branch,omitempty"`
	// Check is the full check QA and releases run.
	Check string `json:"check,omitempty"`
	// Prepare is the ignored paths, or patterns such as **/node_modules,
	// copied into a private copy.
	Prepare []string `json:"prepare,omitempty"`
	// CheckInCopy runs the check in a writable copy of the revision.
	CheckInCopy bool `json:"check_in_copy,omitempty"`
	// CheckLoopback lets the check bind and reach this machine's own
	// addresses, for tests that start a local server; never wider network.
	CheckLoopback bool `json:"check_loopback,omitempty"`
	// Run is how QA starts the app on this machine, or nil.
	Run *RunRecipe `json:"run,omitempty"`
	// Tools are toolchain folders outside the clone the team may read and
	// run from; see Playbook.Tools.
	Tools []string `json:"tools,omitempty"`
	// Areas are named sets of paths in the repository, which projects name
	// in their scope.
	Areas []CodeArea `json:"areas,omitempty"`
}

// CodeArea is a named part of a repository: paths, or patterns, inside it.
type CodeArea struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

// Repository is the repository with this id, to change in place, or nil.
func (v *Snapshot) Repository(id string) *Repository {
	if id == "" {
		return nil
	}
	for i := range v.Repositories {
		if v.Repositories[i].ID == id {
			return &v.Repositories[i]
		}
	}
	return nil
}

// addRepository registers a repository for a path a code project was given
// through its own settings, named after its folder.
func addRepository(v *Snapshot, path string) *Repository {
	v.Repositories = append(v.Repositories, Repository{ID: uid(), Name: filepath.Base(path), Path: path})
	return &v.Repositories[len(v.Repositories)-1]
}

// RepositoryInput is a repository as the owner registers or changes it.
type RepositoryInput struct {
	Name          string     `json:"name"`
	Path          string     `json:"path"`
	DefaultBranch string     `json:"default_branch"`
	Check         string     `json:"check"`
	Prepare       []string   `json:"prepare"`
	CheckInCopy   bool       `json:"check_in_copy"`
	CheckLoopback bool       `json:"check_loopback"`
	Run           *RunRecipe `json:"run"`
	Tools         []string   `json:"tools"`
	Areas         []CodeArea `json:"areas"`
}

func (in RepositoryInput) repository(id, path string) Repository {
	r := Repository{ID: id, Name: strings.TrimSpace(in.Name), Path: path, DefaultBranch: strings.TrimSpace(in.DefaultBranch), Check: strings.TrimSpace(in.Check), CheckInCopy: in.CheckInCopy, CheckLoopback: in.CheckLoopback}
	for _, p := range in.Prepare {
		if p = strings.TrimSpace(p); p != "" {
			r.Prepare = append(r.Prepare, p)
		}
	}
	if in.Run != nil {
		recipe := in.Run.Trimmed()
		r.Run = &recipe
	}
	for _, dir := range in.Tools {
		if dir = strings.TrimSpace(dir); dir != "" && !slices.Contains(r.Tools, dir) {
			r.Tools = append(r.Tools, dir)
		}
	}
	for _, a := range in.Areas {
		area := CodeArea{Name: strings.TrimSpace(a.Name)}
		for _, p := range a.Paths {
			if p = strings.TrimSpace(p); p != "" {
				area.Paths = append(area.Paths, p)
			}
		}
		r.Areas = append(r.Areas, area)
	}
	if r.Name == "" {
		r.Name = filepath.Base(path)
	}
	return r
}

// validate checks a repository on its own; each project working in it
// checks the rest with its playbook.
func (r Repository) validate() error {
	if !filepath.IsAbs(r.Path) {
		return errors.New("a repository needs the absolute path of its local clone")
	}
	if len(r.Name) > 200 {
		return errors.New("a repository's name must fit in 200 characters")
	}
	if b := r.DefaultBranch; b != "" && (strings.ContainsAny(b, " ~^:?*[\\") || strings.Contains(b, "..") || strings.HasPrefix(b, "-") || strings.HasSuffix(b, "/")) {
		return fmt.Errorf("default branch %q is not a branch name", b)
	}
	for _, rel := range r.Prepare {
		if err := validPrepare(rel); err != nil {
			return err
		}
	}
	for _, dir := range r.Tools {
		if err := validTool(dir); err != nil {
			return err
		}
	}
	if r.Run != nil {
		if err := r.Run.validate(); err != nil {
			return err
		}
	}
	names := map[string]bool{}
	for _, a := range r.Areas {
		key := strings.ToLower(a.Name)
		if a.Name == "" || names[key] {
			return errors.New("each code area needs a distinct name")
		}
		names[key] = true
		if len(a.Paths) == 0 {
			return fmt.Errorf("code area %s names no paths", a.Name)
		}
		for _, rel := range a.Paths {
			if !inside(rel) {
				return fmt.Errorf("code area %s: path %q must be inside the repository", a.Name, rel)
			}
		}
	}
	return nil
}

// inside reports a path, or pattern, relative to a repository and within it.
func inside(rel string) bool {
	return !filepath.IsAbs(rel) && !strings.HasPrefix(filepath.Clean(rel), "..")
}

// normalizeRepositoryPath is the path as a project folder is kept: absolute,
// existing, symlinks resolved, and apart from the assistant's own state.
func (s *Service) normalizeRepositoryPath(path string) (string, error) {
	paths, err := s.normalizeProjectDirectories([]string{path})
	if err != nil {
		return "", err
	}
	if len(paths) != 1 {
		return "", errors.New("a repository needs the absolute path of its local clone")
	}
	return paths[0], nil
}

// CreateRepository registers a repository. Nothing works in it until a
// project whose folders include it names it in its scope.
func (s *Service) CreateRepository(ctx context.Context, in RepositoryInput) (Repository, error) {
	path, err := s.normalizeRepositoryPath(in.Path)
	if err != nil {
		return Repository{}, err
	}
	r := in.repository(uid(), path)
	if err := r.validate(); err != nil {
		return Repository{}, err
	}
	err = s.store.update(ctx, func(v *Snapshot) error {
		v.Repositories = append(v.Repositories, r)
		record(v, s.now().UTC(), "", "repository.created", "Repository "+r.Name+" at "+r.Path)
		return nil
	})
	return r, err
}

// UpdateRepository changes a repository for every project working in it.
// Each must still have a usable playbook, and a new path must be one of
// each one's folders. Tasks already under way keep what they started with.
func (s *Service) UpdateRepository(ctx context.Context, id string, in RepositoryInput) (Repository, error) {
	path, err := s.normalizeRepositoryPath(in.Path)
	if err != nil {
		return Repository{}, err
	}
	next := in.repository(id, path)
	if err := next.validate(); err != nil {
		return Repository{}, err
	}
	err = s.store.update(ctx, func(v *Snapshot) error {
		r := v.Repository(id)
		if r == nil {
			return ErrNotFound
		}
		before := snapshotCopy(v)
		using := v.projectsUsing(id, "")
		for _, p := range using {
			if !slices.Contains(p.Directories, path) {
				return fmt.Errorf("%s doesn't have %s among its folders; link it there first", p.Title, path)
			}
			if err := areasKept(p, next); err != nil {
				return err
			}
		}
		*r = next
		if err := settle(before, v, "", id, "", s.validPlaybook); err != nil {
			return err
		}
		now := s.now().UTC()
		for _, p := range using {
			project(v, p.ID).UpdatedAt = now
		}
		record(v, now, "", "repository.updated", "Repository "+r.Name+" changed"+sharedBy(using))
		return nil
	})
	return next, err
}

// areasKept refuses to drop a code area a project's scope names.
func areasKept(p Project, r Repository) error {
	for _, s := range p.Scope.Repositories {
		if s.ID != r.ID {
			continue
		}
		for _, name := range s.Areas {
			if !slices.ContainsFunc(r.Areas, func(a CodeArea) bool { return a.Name == name }) {
				return fmt.Errorf("%s's scope names the code area %s; take it out of the scope first", p.Title, name)
			}
		}
	}
	return nil
}

// sharedBy says which projects a change to a shared repository or team
// reached, for the activity log.
func sharedBy(using []Project) string {
	if len(using) < 2 {
		return ""
	}
	titles := make([]string, len(using))
	for i, p := range using {
		titles[i] = p.Title
	}
	return " for " + strings.Join(titles, ", ")
}

// DeleteRepository removes a repository no project works in.
func (s *Service) DeleteRepository(ctx context.Context, id string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		r := v.Repository(id)
		if r == nil {
			return ErrNotFound
		}
		if using := v.projectsUsing(id, ""); len(using) > 0 {
			return fmt.Errorf("%s still works in %s; give it another repository first: %w", using[0].Title, r.Name, ErrConflict)
		}
		name := r.Name
		v.Repositories = slices.DeleteFunc(v.Repositories, func(r Repository) bool { return r.ID == id })
		record(v, s.now().UTC(), "", "repository.deleted", "Repository "+name+" removed")
		return nil
	})
}
