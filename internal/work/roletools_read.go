package work

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

// designFiles lists the files attached to a design, as read_task shows
// them: on the role's own task, where each is kept, which its turn can
// read; on another task, only their names, since its turn can't open them.
func (r roleTools) designFiles(t core.Task, d core.DesignRequest) string {
	var b strings.Builder
	dir := r.lp.Core.AttachmentsDirectory(t.ID)
	for _, a := range t.Attachments {
		switch {
		case a.Design != d.ID:
		case t.ID == r.taskID:
			fmt.Fprintf(&b, "- design %d file, which you can open: %s (%s)\n", d.N, filepath.Join(dir, a.ID), a.Name)
		default:
			fmt.Fprintf(&b, "- design %d file: %s (on another task, so not readable from your turn)\n", d.N, a.Name)
		}
	}
	return b.String()
}

func (r roleTools) list(ctx context.Context, which, relatedTo, contains string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	var related core.Task
	if relatedTo != "" {
		var ok bool
		if related, ok = findTask(snap, r.projectID, relatedTo); !ok {
			return "", errNoTask
		}
	}
	var b strings.Builder
	for _, t := range snap.Tasks {
		switch {
		case t.ProjectID != r.projectID:
			continue
		case (which == "" || which == "unfinished") && t.Finished():
			continue
		case which == "finished" && !t.Finished():
			continue
		case relatedTo != "" && !linkedTo(related, t):
			continue
		case contains != "" && !strings.Contains(strings.ToLower(t.Objective), strings.ToLower(contains)):
			continue
		}
		mine := ""
		if t.ID == r.taskID {
			mine = " (your task)"
		}
		fmt.Fprintf(&b, "- %s (%s)%s: %s%s\n", t.Label(), t.Status, mine, text.Clip(t.Objective, 200), linksLine(snap, t))
	}
	if b.Len() == 0 {
		return "No tasks match.", nil
	}
	return b.String(), nil
}

// linkGroup is one kind of link a task has, with the tasks at the other end.
type linkGroup struct {
	name string
	ids  []string
}

func linkGroups(t core.Task) []linkGroup {
	return []linkGroup{{"depends on", t.DependsOn}, {"blocks", t.Blocks}, {"relates to", t.RelatesTo}}
}

func linkedTo(a, b core.Task) bool {
	return slices.ContainsFunc(linkGroups(a), func(g linkGroup) bool { return slices.Contains(g.ids, b.ID) })
}

func linksLine(snap core.Snapshot, t core.Task) string {
	parts := blockerLines(t)
	for _, l := range linkGroups(t) {
		if len(l.ids) > 0 {
			names := make([]string, len(l.ids))
			for i, id := range l.ids {
				names[i] = id
				if o, ok := snap.FindTask(id); ok {
					names[i] = o.Label()
				}
			}
			parts = append(parts, l.name+" "+strings.Join(names, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, "; ") + "]"
}

// notes reads a page of a task's notes: from the one numbered from, or the
// latest.
func (r roleTools) notes(ctx context.Context, id, from string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	t, ok := findTask(snap, r.projectID, id)
	if !ok {
		return "", errNoTask
	}
	first := len(t.Notes) - maxNotesPage + 1
	if from = strings.TrimSpace(from); from != "" {
		if first, err = strconv.Atoi(from); err != nil || first < 1 {
			return "", errors.New("from is the number of a note, such as 1, or empty for the latest")
		}
	}
	return notesPage(t, first, maxNotesPage), nil
}

func (r roleTools) read(ctx context.Context, id string) (string, error) {
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	t, ok := findTask(snap, r.projectID, id)
	if !ok {
		return "", errNoTask
	}
	files := ""
	if current, ok := t.CurrentDesignInput(); ok {
		files = r.designFiles(t, current)
	}
	return taskBrief(snap, r.projectID, t, files), nil
}

// taskBrief is task t as read_task shows it, with the files of its current
// design, and the tasks it links to in projectID alone.
func taskBrief(snap core.Snapshot, projectID string, t core.Task, designFiles string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s, %s): %s\n", t.Label(), t.Status, t.Stage, text.Clip(t.Objective, 600))
	for _, line := range blockerLines(t) {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	for _, c := range t.Criteria {
		fmt.Fprintf(&b, "- criterion: %s\n", text.Clip(c, 300))
	}
	if n := len(t.Edits); n > 0 {
		fmt.Fprintf(&b, "- title or requirements changed %d times, last by %s\n", n, t.Edits[n-1].By)
	}
	for _, l := range linkGroups(t) {
		for _, other := range l.ids {
			if o, ok := findTask(snap, projectID, other); ok {
				fmt.Fprintf(&b, "- %s %s (%s): %s\n", l.name, o.Label(), o.Status, text.Clip(o.Objective, 200))
			}
		}
	}
	if t.Plan != nil {
		fmt.Fprintf(&b, "Plan: %s\n", text.Clip(t.Plan.Summary, 800))
		if t.Plan.NeedsDesigner != "" {
			fmt.Fprintf(&b, "Needs visual design, but no designer is on the team: %s\n", t.Plan.NeedsDesigner)
		}
		for _, c := range t.Plan.Changes {
			fmt.Fprintf(&b, "- change: %s\n", text.Clip(c, 300))
		}
		for _, o := range t.Plan.OutOfScope {
			fmt.Fprintf(&b, "- out of scope: %s\n", text.Clip(o, 300))
		}
	}
	for _, q := range t.Research {
		fmt.Fprintf(&b, "- %s asked for more research on draft %d: %s\n", q.From, q.Revision, text.Clip(q.Question, 300))
	}
	if current, ok := t.CurrentDesignInput(); ok {
		fmt.Fprintf(&b, "Current design, the target: design %d by %s: %s\n", current.N, current.Designer, text.Clip(current.Input, 800))
		b.WriteString(designFiles)
	}
	var superseded []string
	for _, d := range t.Design {
		if d.Marked && d.ID != t.CurrentDesign {
			superseded = append(superseded, strconv.Itoa(d.N))
		}
	}
	if len(superseded) > 0 {
		fmt.Fprintf(&b, "- superseded designs, not the target: %s\n", strings.Join(superseded, ", "))
	}
	if n := len(t.Attachments); n > 0 {
		fmt.Fprintf(&b, "Attachments: %d\n", n)
	}
	if n := len(t.Revisions); n > 0 {
		last := t.Revisions[n-1]
		fmt.Fprintf(&b, "Latest draft %d: %s\n", last.N, text.Clip(last.Summary, 800))
		for _, v := range t.Verdicts {
			if v.Revision != last.N || v.Answered {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s%s %s\n", v.Role, v.Outcome, checkedRef(v, last), text.Clip(v.Summary, 300))
			b.WriteString(evidenceText(t, v, "  ", 300))
			if v.Next != "" || v.Note != "" {
				fmt.Fprintf(&b, "  recommends %s: %s\n", orDash(v.Next), orDash(v.Note))
			}
		}
	}
	if len(t.Notes) > 0 {
		b.WriteString("Notes:\n" + latestNotes(t))
	}
	if t.Branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", t.Branch)
	}
	return b.String()
}

func blockerLines(t core.Task) []string {
	var out []string
	for _, b := range t.Blockers {
		if b.ClearedAt != nil {
			continue
		}
		line := fmt.Sprintf("held until: %s (blocker %s, %s)", b.Description, b.ID, b.Kind)
		if b.LandingOnly {
			line += " (landing only)"
		}
		switch b.By {
		case core.LinkedByOwner:
			line += " (set by the owner)"
		case core.LinkedByAssistant:
			line += " (set by the assistant)"
		}
		if b.Check != "" {
			line += ": " + b.Check
		}
		out = append(out, line)
	}
	return out
}
