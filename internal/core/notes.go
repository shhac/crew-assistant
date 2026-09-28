package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// Note is something a team member, the owner or the assistant left on a
// task for whoever works on it next. Notes are a shared channel beside the
// record: roles still judge from the record itself (the objective and
// criteria, the drafts and the verdicts), never from a note's account of it.
type Note struct {
	ID string `json:"id"`
	// By is who wrote it: a team member's seat name, or the owner or the
	// assistant; Kind is the role they wrote as, or FromOwner or
	// FromAssistant.
	By   string    `json:"by"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// NoteInput is a note to leave on a task.
type NoteInput struct {
	Project, Task string
	By, Kind      string
	// While, when set, is the status the task must still be in, as for a
	// role's other changes.
	While string
	Text  string
}

// Limits on a task's notes.
const (
	maxNote  = 2000
	maxNotes = 200
)

// AddNote leaves a note on a task. The team leaves none on a finished task;
// the owner and the assistant may.
func (s *Service) AddNote(ctx context.Context, in NoteInput) (Note, error) {
	words := strings.TrimSpace(in.Text)
	if words == "" {
		return Note{}, errors.New("a note needs some text")
	}
	var out Note
	err := s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, strings.TrimSpace(in.Task))
		switch {
		case t == nil || t.ProjectID != in.Project:
			return ErrNotFound
		case in.While != "" && t.Status != in.While:
			return fmt.Errorf("the task has moved on, so this changes nothing more: %w", ErrConflict)
		case !overrules(in.Kind) && t.Finished():
			return fmt.Errorf("“%s” has finished, so the team no longer changes it: %w", t.Objective, ErrConflict)
		case len(t.Notes) >= maxNotes:
			return fmt.Errorf("this task already has %d notes: %w", maxNotes, ErrConflict)
		}
		now := s.now().UTC()
		out = Note{ID: uid(), By: in.By, Kind: in.Kind, Text: text.Clip(words, maxNote), At: now}
		t.Notes = append(t.Notes, out)
		t.UpdatedAt = now
		recordTask(v, now, t, "task.note", fmt.Sprintf("%s left a note on %s: %s", in.By, t.Objective, text.Clip(words, 200)))
		derive(v, t)
		return nil
	})
	return out, err
}
