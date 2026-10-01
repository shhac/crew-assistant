package core

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
)

type LinearUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type LinearRules struct {
	PickUp   bool         `json:"pick_up"`
	States   []string     `json:"states"`
	Assignee string       `json:"assignee"`
	Users    []LinearUser `json:"users"`
}
type LinearLink struct {
	ConnectionID string      `json:"connection_id"`
	Profile      string      `json:"profile"`
	Kind         string      `json:"kind"`
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Rules        LinearRules `json:"rules"`
	Version      int         `json:"version"`
	Cursor       string      `json:"cursor,omitempty"`
	LastError    string      `json:"last_error,omitempty"`
}
type LinearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

var linearUUID = regexp.MustCompile(`^([0-9a-fA-F]{32}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// ValidLinearID checks UUIDs used in project links and option reads.
func ValidLinearID(id string) bool { return linearUUID.MatchString(id) }

var errLinearUnchanged = errors.New("Linear record unchanged")

func LinearBinding(bindings []config.Connection, id, profile string) error {
	for _, b := range bindings {
		if b.ID == id && b.Tool == "lin" {
			for _, p := range b.Profiles {
				if p == profile {
					return nil
				}
			}
			return errors.New("the Linear connection this project used is gone")
		}
	}
	return errors.New("the Linear connection this project used is gone")
}
func validLinearValue(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\x00\r\n") && !strings.HasPrefix(s, "-")
}
func ValidateLinear(l LinearLink) error {
	if (l.Kind != "team" && l.Kind != "project") || !ValidLinearID(l.ID) || !validLinearValue(l.Name) {
		return errors.New("choose a Linear team or project")
	}
	if len(l.Rules.States) > 100 || len(l.Rules.Users) > 100 {
		return errors.New("too many pick-up rules")
	}
	for _, s := range l.Rules.States {
		if !validLinearValue(s) {
			return errors.New("invalid status")
		}
	}
	switch l.Rules.Assignee {
	case "any", "unassigned", "me":
	case "users":
		if len(l.Rules.Users) == 0 {
			return errors.New("choose at least one user")
		}
		for _, u := range l.Rules.Users {
			if !ValidLinearID(u.ID) || !validLinearValue(u.Name) {
				return errors.New("invalid user")
			}
		}
	default:
		return errors.New("choose an assignee rule")
	}
	return nil
}

type LinearRef struct {
	LinearIssue
	Kind         string    `json:"kind"`
	ConnectionID string    `json:"connection_id"`
	Profile      string    `json:"profile"`
	By           string    `json:"by"`
	At           time.Time `json:"at"`
}

// ClearProjectLinear stops pick-up but retains the tasks and issue receipts.
func (s *Service) ClearProjectLinear(ctx context.Context, id string) (Project, error) {
	return s.SetProjectLinear(ctx, id, nil)
}

// SetProjectLinearCursor resumes a bounded scan without sending every receipt
// to Linear. Repeating a page after a restart is safe because imports are atomic.
func (s *Service) SetProjectLinearCursor(ctx context.Context, id string, version int, cursor string) error {
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		if p.Linear == nil || p.Linear.Version != version || !p.Linear.Rules.PickUp {
			return ErrConflict
		}
		if p.Linear.Cursor == cursor {
			return errLinearUnchanged
		}
		p.Linear.Cursor = cursor
		return nil
	})
	if errors.Is(err, errLinearUnchanged) {
		return nil
	}
	return err
}

func (s *Service) SetProjectLinear(ctx context.Context, id string, l *LinearLink) (Project, error) {
	if l != nil {
		if err := LinearBinding(s.configuration().Connections, l.ConnectionID, l.Profile); err != nil {
			return Project{}, err
		}
		if err := ValidateLinear(*l); err != nil {
			return Project{}, err
		}
	}
	var out Project
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		p.LinearVersion++
		if l != nil {
			copy := *l
			copy.Version = p.LinearVersion
			copy.LastError = ""
			copy.Cursor = ""
			p.Linear = &copy
		} else {
			p.Linear = nil
		}
		out = *p
		record(v, s.now().UTC(), id, "project.linear", "Linear link updated")
		return nil
	})
	return out, err
}
func (s *Service) LinearError(ctx context.Context, id string, version int, message string) error {
	err := s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		if p.Linear == nil || p.Linear.Version != version {
			return errLinearUnchanged
		}
		if p.Linear.LastError == message {
			return errLinearUnchanged
		}
		if p.Linear.LastError != message {
			p.Linear.LastError = message
			summary := message
			if summary == "" {
				summary = "Linear pick-up recovered"
			}
			record(v, s.now().UTC(), id, "project.linear-status", summary)
		}
		return nil
	})
	if errors.Is(err, errLinearUnchanged) {
		return nil
	}
	return err
}
func (s *Service) PickUpLinearIssue(ctx context.Context, id string, version int, i LinearIssue) (bool, error) {
	u, err := url.Parse(i.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || i.ID == "" || i.Identifier == "" || i.Title == "" {
		return false, errors.New("Linear returned an incomplete issue")
	}
	made := false
	err = s.store.update(ctx, func(v *Snapshot) error {
		p := project(v, id)
		if p == nil {
			return ErrNotFound
		}
		l := p.Linear
		if l == nil || l.Version != version || !l.Rules.PickUp {
			return ErrConflict
		}
		if err := LinearBinding(s.configuration().Connections, l.ConnectionID, l.Profile); err != nil {
			return err
		}
		key := i.ID
		if slices.Contains(p.LinearImported, key) {
			return errLinearUnchanged
		}
		now := s.now().UTC()
		t := Task{ID: uid(), ProjectID: id, Objective: i.Identifier + ": " + i.Title, Criteria: []string{}, Status: TaskQueued, Stage: StageTodo, Revisions: []Revision{}, Verdicts: []Verdict{}, CreatedAt: now, UpdatedAt: now, Linear: []LinearRef{{LinearIssue: i, Kind: "issue", ConnectionID: l.ConnectionID, Profile: l.Profile, By: LinkedByAssistant, At: now}}}
		if err := queueTask(v, p, &t, nil, LinkedByAssistant, "task.picked-up"); err != nil {
			return err
		}
		p.LinearImported = append(p.LinearImported, key)
		made = true
		return nil
	})
	if errors.Is(err, errLinearUnchanged) {
		err = nil
	}
	return made, err
}
