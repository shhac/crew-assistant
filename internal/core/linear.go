package core

import (
	"context"
	"errors"
	"fmt"
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

// ValidateTaskLinear checks metadata returned by a read-only lookup.
func ValidateTaskLinear(ref LinearRef) error {
	u, err := url.Parse(ref.URL)
	if (ref.Kind != "issue" && ref.Kind != "project") || !ValidLinearID(ref.ID) ||
		!validLinearValue(ref.Identifier) || !validLinearValue(ref.Title) ||
		err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		(u.Hostname() != "linear.app" && !strings.HasSuffix(u.Hostname(), ".linear.app")) {
		return errors.New("Linear returned an invalid link")
	}
	return nil
}

func (s *Service) AddTaskLinear(ctx context.Context, projectID, taskID string, ref LinearRef, by string) (Task, error) {
	var unchanged Task
	out, err := s.editTaskRecord(ctx, projectID, taskID, func(t *Task, v *Snapshot) error {
		p := project(v, t.ProjectID)
		if p == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		summary, err := s.linkTaskLinear(p, t, ref, by, now)
		if err != nil {
			return err
		}
		if summary == "" {
			unchanged = *t
			return errLinearUnchanged
		}
		t.UpdatedAt = now
		recordTask(v, now, t, "task.linear-linked", summary)
		return nil
	})
	if errors.Is(err, errLinearUnchanged) {
		return unchanged, nil
	}
	return out, err
}

// QueueLinearTask queues a task linked to a Linear issue in one change, so
// the task never exists without the issue it was asked for. An issue already
// linked to one of the project's unfinished tasks is refused rather than
// queued twice.
func (s *Service) QueueLinearTask(ctx context.Context, projectID string, in TaskInput, ref LinearRef, by string) (Task, error) {
	if ref.Kind != "issue" {
		return Task{}, errors.New("a task is queued for a Linear issue, not a project")
	}
	return s.queueTaskAs(ctx, projectID, in, by, func(v *Snapshot, p *Project, t *Task) (string, error) {
		for _, other := range v.Tasks {
			if other.ProjectID == p.ID && !other.Finished() && slices.ContainsFunc(other.LinearIssues(), func(l LinearRef) bool { return l.ID == ref.ID }) {
				return "", fmt.Errorf("%s is already linked to %s", ref.Identifier, other.Label())
			}
		}
		return s.linkTaskLinear(p, t, ref, by, t.CreatedAt)
	})
}

// linkTaskLinear adds ref to the task's links, or refreshes the description
// a link to the same issue holds, and says what changed: nothing, when it
// gives no summary.
func (s *Service) linkTaskLinear(p *Project, t *Task, ref LinearRef, by string, now time.Time) (string, error) {
	if err := mayLinkLinear(p, ref, by); err != nil {
		return "", err
	}
	if err := LinearBinding(s.configuration().Connections, ref.ConnectionID, ref.Profile); err != nil {
		return "", err
	}
	if err := ValidateTaskLinear(ref); err != nil {
		return "", err
	}
	if ref.Kind != "issue" {
		t.LinearLinks = slices.DeleteFunc(t.LinearLinks, func(l LinearRef) bool { return l.Kind == "project" })
		ref.Description = ""
		ref.By, ref.At = by, now
		t.LinearLinks = append(t.LinearLinks, ref)
		return "Linear project " + ref.Identifier + " linked", nil
	}
	if slices.ContainsFunc(t.Linear, func(l LinearRef) bool { return l.ID == ref.ID }) {
		return "", nil
	}
	count := 0
	for i, l := range t.LinearLinks {
		if l.Kind != "issue" {
			continue
		}
		count++
		if l.ID != ref.ID {
			continue
		}
		// Linking an issue again brings the team its current description.
		if ref.Description == "" || ref.Description == l.Description {
			return "", nil
		}
		t.LinearLinks[i].Description = ref.Description
		return "Linear issue " + l.Identifier + " description refreshed", nil
	}
	if count >= 50 {
		return "", errors.New("a task can have at most 50 Linear issue links")
	}
	if !slices.Contains(p.LinearImported, ref.ID) {
		p.LinearImported = append(p.LinearImported, ref.ID)
	}
	ref.By, ref.At = by, now
	t.LinearLinks = append(t.LinearLinks, ref)
	return "Linear issue " + ref.Identifier + " linked", nil
}

// mayLinkLinear is whether by may link a task to ref. The PM reaches Linear
// only through its project's own connection, so it links only what that
// connection read, and only while the project still uses it.
func mayLinkLinear(p *Project, ref LinearRef, by string) error {
	switch by {
	case LinkedByOwner, LinkedByAssistant:
		return nil
	case LinkedByPM:
		if p.Linear == nil || p.Linear.ConnectionID != ref.ConnectionID || p.Linear.Profile != ref.Profile {
			return errors.New("this project is no longer linked to that Linear account")
		}
		return nil
	}
	return errors.New("only the owner, assistant or PM can link Linear")
}

// LinearIssues are the Linear issues a task is for: the one it was picked up
// from, then those linked to it, each once.
func (t Task) LinearIssues() []LinearRef {
	var out []LinearRef
	for _, l := range slices.Concat(t.Linear, t.LinearLinks) {
		if l.Kind == "issue" && !slices.ContainsFunc(out, func(o LinearRef) bool { return o.ID == l.ID }) {
			out = append(out, l)
		}
	}
	return out
}

var linearTicket = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

// LinearTicket reports an issue identifier in Linear's team-key form, such
// as EX-123, which is safe to name a branch or a pull request reference by.
func LinearTicket(identifier string) bool { return linearTicket.MatchString(identifier) }

func (s *Service) RemoveTaskLinear(ctx context.Context, projectID, taskID, kind, id, by string) (Task, error) {
	return s.editTaskRecord(ctx, projectID, taskID, func(t *Task, v *Snapshot) error {
		if project(v, t.ProjectID) == nil {
			return ErrNotFound
		}
		if by != LinkedByOwner && by != LinkedByAssistant {
			return errors.New("only the owner or assistant can unlink Linear")
		}
		for n, l := range t.LinearLinks {
			if l.Kind == kind && l.ID == id {
				t.LinearLinks = slices.Delete(t.LinearLinks, n, n+1)
				t.UpdatedAt = s.now().UTC()
				recordTask(v, t.UpdatedAt, t, "task.linear-unlinked", "Linear "+kind+" "+l.Identifier+" unlinked")
				return nil
			}
		}
		return ErrNotFound
	})
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
