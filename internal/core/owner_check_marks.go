package core

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// OwnerCheckMark is an append-only owner action. Undo records outstanding.
type OwnerCheckMark struct {
	Step   string    `json:"step"`
	Status string    `json:"status"`
	Note   string    `json:"note,omitempty"`
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Bulk   bool      `json:"bulk,omitempty"`
}
type OwnerCheckItem struct {
	Step    string           `json:"step"`
	Status  string           `json:"status"`
	Mark    *OwnerCheckMark  `json:"mark,omitempty"`
	History []OwnerCheckMark `json:"history,omitempty"`
}
type OutstandingCheck struct {
	ProjectID string    `json:"project_id"`
	Project   string    `json:"project"`
	TaskID    string    `json:"task_id"`
	Ref       string    `json:"ref"`
	Objective string    `json:"objective"`
	Step      string    `json:"step"`
	LandedAt  time.Time `json:"landed_at"`
}

func (t Task) ownerSteps() []string {
	var steps []string
	for _, step := range append(slices.Clone(t.OwnerChecks), t.OwnerSteps...) {
		if !slices.Contains(steps, step) {
			steps = append(steps, step)
		}
	}
	return steps
}
func (t Task) ownerChecklist() []OwnerCheckItem {
	var items []OwnerCheckItem
	for _, step := range t.ownerSteps() {
		item := OwnerCheckItem{Step: step, Status: "outstanding"}
		for _, mark := range t.CheckMarks {
			if mark.Step != step {
				continue
			}
			if item.Mark != nil {
				item.History = append(item.History, *item.Mark)
			}
			m := mark
			item.Mark, item.Status = &m, m.Status
		}
		items = append(items, item)
	}
	return items
}
func afterLanding(t Task, legacy time.Time) bool {
	return t.Status == TaskLanded || t.Status == TaskDelivered ||
		(t.Active() || t.Status == TaskWaiting || t.Status == TaskAwaiting) && (!t.LandedAt.IsZero() || !legacy.IsZero())
}

// Status activity can also describe later edits. The first recorded landing
// is the best available time for tasks predating LandedAt.
func ownerCheckLandings(v *Snapshot) map[string]time.Time {
	landings := map[string]time.Time{}
	for _, a := range v.Activity {
		if a.Kind != "task.landed" && a.Kind != "task.delivered" {
			continue
		}
		if at := landings[a.TaskID]; at.IsZero() || a.CreatedAt.Before(at) {
			landings[a.TaskID] = a.CreatedAt
		}
	}
	return landings
}
func checkActor(by string) error {
	if by != FromOwner && by != FromAssistant {
		return errors.New("only the owner or assistant can mark owner checks")
	}
	return nil
}
func checkNote(note string) (string, error) {
	note = text.Clip(strings.TrimSpace(note), maxCriterion)
	if note == "" {
		return "", errors.New("a short note is required to skip checks")
	}
	return note, nil
}

// MarkOwnerCheck changes exactly one step, without changing task progress.
func (s *Service) MarkOwnerCheck(ctx context.Context, projectID, taskID, step, status, note, by string) (Task, error) {
	var out Task
	if err := checkActor(by); err != nil {
		return out, err
	}
	switch status {
	case "skipped":
		var err error
		note, err = checkNote(note)
		if err != nil {
			return out, err
		}
	case "done", "outstanding":
		note = ""
	default:
		return out, errors.New("status must be done, skipped or outstanding")
	}
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, taskID)
		if t == nil || t.ProjectID != projectID {
			return ErrNotFound
		}
		if !afterLanding(*t, ownerCheckLandings(v)[t.ID]) {
			return ErrConflict
		}
		found := false
		for _, item := range t.ownerChecklist() {
			if item.Step != step {
				continue
			}
			found = true
			if item.Status != status {
				now := s.now().UTC()
				t.CheckMarks = append(t.CheckMarks, OwnerCheckMark{Step: step, Status: status, Note: note, By: by, At: now})
				recordTask(v, now, t, "task.owner_check", step+": "+status)
			}
		}
		if !found {
			return ErrNotFound
		}
		out = *t
		out.Checklist = t.ownerChecklist()
		return nil
	})
	return out, err
}

// ClearOwnerChecks skips only currently outstanding checks in one transaction.
func (s *Service) ClearOwnerChecks(ctx context.Context, projectID, note, by string) (int, error) {
	if err := checkActor(by); err != nil {
		return 0, err
	}
	note, err := checkNote(note)
	if err != nil {
		return 0, err
	}
	count := 0
	err = s.store.update(ctx, func(v *Snapshot) error {
		if project(v, projectID) == nil {
			return ErrNotFound
		}
		now := s.now().UTC()
		landings := ownerCheckLandings(v)
		for i := range v.Tasks {
			t := &v.Tasks[i]
			if t.ProjectID != projectID || !afterLanding(*t, landings[t.ID]) {
				continue
			}
			for _, item := range t.ownerChecklist() {
				if item.Status != "outstanding" {
					continue
				}
				t.CheckMarks = append(t.CheckMarks, OwnerCheckMark{Step: item.Step, Status: "skipped", Note: note, By: by, At: now, Bulk: true})
				count++
			}
		}
		if count > 0 {
			record(v, now, projectID, "project.owner_checks", "Outstanding checks skipped: "+note)
		}
		return nil
	})
	return count, err
}

func deriveOwnerChecks(v *Snapshot) {
	v.AfterLanding = []OutstandingCheck{}
	landings := ownerCheckLandings(v)
	for i := range v.Tasks {
		t := &v.Tasks[i]
		t.Checklist = t.ownerChecklist()
		if !afterLanding(*t, landings[t.ID]) || len(t.Checklist) == 0 {
			continue
		}
		at := t.LandedAt
		if at.IsZero() {
			at = landings[t.ID]
		}
		if at.IsZero() {
			at = t.UpdatedAt
		}
		// Enrich legacy reads without backfilling stored tasks. This also
		// keeps their checklist visible while a delivered task lands again.
		t.LandedAt = at
		title := ""
		if p := project(v, t.ProjectID); p != nil {
			title = p.Title
		}
		for _, item := range t.Checklist {
			if item.Status == "outstanding" {
				v.AfterLanding = append(v.AfterLanding, OutstandingCheck{ProjectID: t.ProjectID, Project: title, TaskID: t.ID, Ref: t.Ref, Objective: t.Objective, Step: item.Step, LandedAt: at})
			}
		}
	}
	// Stable sorting preserves checklist position within a task.
	sort.SliceStable(v.AfterLanding, func(i, j int) bool {
		a, b := v.AfterLanding[i], v.AfterLanding[j]
		if !a.LandedAt.Equal(b.LandedAt) {
			return a.LandedAt.Before(b.LandedAt)
		}
		return a.TaskID < b.TaskID
	})
}
