package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shhac/crew-assistant/internal/statepath"
)

// StateDirectory is derived from the actual opened database, not config defaults.
func (s *Service) StateDirectory() string { return s.store.stateDirectory }

func (s *Store) prepareProject(p *Project) error {
	scratch, err := statepath.EnsureDirectory(s.stateDirectory, "projects", p.ID)
	if err != nil {
		return fmt.Errorf("prepare project scratch directory: %w", err)
	}
	p.ScratchDirectory = scratch
	if p.Directories == nil {
		p.Directories = []string{}
	}
	return nil
}

func (s *Service) normalizeProjectDirectories(directories []string) ([]string, error) {
	if len(directories) > 16 {
		return nil, errors.New("a project can link at most 16 directories")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			return nil, errors.New("project directories must be absolute paths")
		}
		canonical, err := filepath.EvalSymlinks(filepath.Clean(directory))
		if err != nil {
			return nil, fmt.Errorf("resolve project directory %q: %w", directory, err)
		}
		info, err := os.Stat(canonical)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("project path %q is not a directory", directory)
		}
		// Private model/scratch files must never be written inside a linked repository.
		if pathContains(canonical, s.StateDirectory()) || pathContains(s.StateDirectory(), canonical) {
			return nil, errors.New("project directories must be separate from the assistant state directory")
		}
		key := canonical
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			out = append(out, canonical)
			seen[key] = true
		}
	}
	return out, nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// SetProjectDirectories changes references only; no project files are opened.
func (s *Service) SetProjectDirectories(ctx context.Context, id string, directories []string) (Project, error) {
	normalized, err := s.normalizeProjectDirectories(directories)
	if err != nil {
		return Project{}, err
	}
	var out Project
	err = s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		p.Directories = normalized
		p.UpdatedAt = s.now().UTC()
		out = *p
		record(v, p.UpdatedAt, id, "project.directories_updated", "Linked directories updated for "+p.Title)
		return nil
	})
	return out, err
}

func (s *Service) CreateProject(ctx context.Context, in ProjectInput) (Project, error) {
	if !required(in.Title) {
		return Project{}, errors.New("title is required")
	}
	directories, err := s.normalizeProjectDirectories(in.Directories)
	if err != nil {
		return Project{}, err
	}
	now := s.now().UTC()
	out := Project{ID: uid(), Title: in.Title, Status: "active", Directories: directories, SourceID: in.SourceID, SourceDescription: in.SourceDescription, UpdatedAt: now}
	if required(in.Brief.Goal) {
		out.Brief = Brief{Version: 1, Goal: strings.TrimSpace(in.Brief.Goal), Audience: strings.TrimSpace(in.Brief.Audience), Constraints: strings.TrimSpace(in.Brief.Constraints), Criteria: cleanList(in.Brief.Criteria), UpdatedAt: now}
	}
	if in.Template != "" {
		template, ok := Templates[in.Template]
		if !ok {
			return Project{}, fmt.Errorf("unknown project template %q", in.Template)
		}
		template.Roles = append([]Role(nil), template.Roles...)
		out.Playbook = &template
	}
	err = s.store.update(ctx, func(v *Snapshot) error {
		if in.SourceID != "" {
			for i := range v.Projects {
				p := &v.Projects[i]
				if p.SourceID != in.SourceID {
					continue
				}
				// A source refresh updates what the source says, never the brief
				// the owner and assistant have since agreed.
				if p.Title != in.Title || p.SourceDescription != in.SourceDescription {
					p.Title = in.Title
					p.SourceDescription = in.SourceDescription
					p.UpdatedAt = now
				}
				out = *p
				return nil
			}
		}
		if err := s.store.prepareProject(&out); err != nil {
			return err
		}
		v.Projects = append(v.Projects, out)
		record(v, now, out.ID, "project.created", out.Title)
		return nil
	})
	return out, err
}
