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
	out := Project{Directories: directories, ContractDefined: in.SourceID == "" && required(in.AcceptanceCriteria), SourceDescription: in.Description, ID: uid(), Title: in.Title, Description: in.Description, AcceptanceCriteria: in.AcceptanceCriteria, Status: "ready", SourceID: in.SourceID, UpdatedAt: s.now().UTC()}
	err = s.store.update(ctx, func(v *Snapshot) error {
		if in.SourceID != "" {
			for i := range v.Projects {
				p := &v.Projects[i]
				if p.SourceID == in.SourceID {
					if p.Title != in.Title || p.SourceDescription != in.Description {
						p.Title = in.Title
						p.SourceDescription = in.Description
						if !p.ContractDefined {
							p.Description = in.Description
						}
						p.UpdatedAt = s.now().UTC()
					}
					// Source refreshes cannot replace the commissioned acceptance contract.
					out = *p
					return nil
				}
			}
		}
		if err := s.store.prepareProject(&out); err != nil {
			return err
		}
		v.Projects = append(v.Projects, out)
		record(v, out.UpdatedAt, out.ID, "project.created", out.Title)
		return nil
	})
	return out, err
}

// RefineProject establishes the acceptance contract at intake. Once any work
// has been commissioned, changing it requires an explicit new commission.
func (s *Service) RefineProject(ctx context.Context, id, description, acceptanceCriteria string) (Project, error) {
	return s.RefineProjectWithDirectories(ctx, id, description, acceptanceCriteria, nil)
}

// RefineProjectWithDirectories atomically refines a contract and, when non-nil,
// replaces linked directory metadata. Linking never grants worker access.
func (s *Service) RefineProjectWithDirectories(ctx context.Context, id, description, acceptanceCriteria string, directories []string) (Project, error) {
	if !required(description, acceptanceCriteria) {
		return Project{}, errors.New("description and measurable acceptance criteria are required")
	}
	var normalized []string
	var err error
	if directories != nil {
		normalized, err = s.normalizeProjectDirectories(directories)
		if err != nil {
			return Project{}, err
		}
	}
	var out Project
	err = s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		if p.Status != "ready" {
			return errors.New("only an uncommissioned ready project can be refined")
		}
		for _, a := range v.Agents {
			if a.ProjectID == id {
				return errors.New("acceptance contract is frozen after work is commissioned")
			}
		}
		if directories != nil {
			p.Directories = normalized
		}
		p.Description = description
		p.AcceptanceCriteria = acceptanceCriteria
		p.ContractDefined = true
		p.UpdatedAt = s.now().UTC()
		out = *p
		record(v, p.UpdatedAt, id, "project.refined", "Acceptance criteria defined for "+p.Title)
		return nil
	})
	return out, err
}
