package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Readable task IDs. Each project has a short prefix and numbers its tasks
// in the order they were asked for, so a task reads as CA-12. Only the
// prefix and the number are stored: the readable ID is derived from them,
// so renaming a prefix renames every task's readable ID, while links,
// branches and URLs keep holding the canonical ID.

// maxPrefix is the longest prefix a project may have.
const maxPrefix = 6

var prefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{0,5}$`)

// ErrPrefix means a prefix is not letters and digits starting with a letter,
// at most six long.
var ErrPrefix = errors.New("a task ID prefix is 1 to 6 letters or digits, starting with a letter")

// TaskRef is the readable ID of the project's task numbered n, or empty
// while either is unset.
func (p Project) TaskRef(n int) string {
	if p.Prefix == "" || n <= 0 {
		return ""
	}
	return p.Prefix + "-" + strconv.Itoa(n)
}

// Label is how a task is named in text for models and the CLI: its readable
// ID with the canonical one, or the canonical one alone before it has a
// readable one.
func (t Task) Label() string {
	if t.Ref == "" {
		return t.ID
	}
	return t.Ref + " (" + t.ID + ")"
}

// CleanPrefix is a prefix as it is stored: trimmed and upper case.
func CleanPrefix(prefix string) (string, error) {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if !prefixPattern.MatchString(prefix) {
		return "", ErrPrefix
	}
	return prefix, nil
}

// prefixTaken says whether a project other than except already uses prefix.
func prefixTaken(v *Snapshot, prefix, except string) bool {
	return slices.ContainsFunc(v.Projects, func(p Project) bool {
		return p.ID != except && strings.EqualFold(p.Prefix, prefix)
	})
}

// titleWords splits a title into words at anything but letters and digits
// and where lower case turns to upper, as in camelCase.
func titleWords(title string) []string {
	var words []string
	var word []rune
	var last rune
	flush := func() {
		if len(word) > 0 {
			words = append(words, string(word))
			word = nil
		}
	}
	for _, r := range title {
		if !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r))) {
			flush()
			last = 0
			continue
		}
		if unicode.IsUpper(r) && unicode.IsLower(last) {
			flush()
		}
		word = append(word, r)
		last = r
	}
	flush()
	return words
}

// defaultPrefix is the prefix a title suggests: its words' initials, or the
// first three letters of a single word.
func defaultPrefix(title string) string {
	words := titleWords(title)
	// A prefix starts with a letter, so leading numbers don't count.
	for len(words) > 0 && !unicode.IsLetter(rune(words[0][0])) {
		words = words[1:]
	}
	var prefix string
	switch len(words) {
	case 0:
		return "P"
	case 1:
		prefix = words[0][:min(3, len(words[0]))]
	default:
		for _, w := range words {
			prefix += w[:1]
		}
	}
	return strings.ToUpper(prefix[:min(maxPrefix, len(prefix))])
}

// uniquePrefix is the title's default prefix, or, if another project has
// it, that prefix with the title's next letter or a number added.
func uniquePrefix(v *Snapshot, title string) string {
	base := defaultPrefix(title)
	if !prefixTaken(v, base, "") {
		return base
	}
	stem := base[:min(maxPrefix-1, len(base))]
	letters := strings.ToUpper(strings.Join(titleWords(title), ""))
	if letters != "" {
		letters = letters[1:]
	}
	for _, r := range letters {
		if candidate := stem + string(r); !prefixTaken(v, candidate, "") {
			return candidate
		}
	}
	for n := 2; ; n++ {
		digits := strconv.Itoa(n)
		candidate := base[:min(maxPrefix-len(digits), len(base))] + digits
		if !prefixTaken(v, candidate, "") {
			return candidate
		}
	}
}

// numberTask gives a new task its project's next number.
func numberTask(p *Project, t *Task) {
	if p.NextTask < 1 {
		p.NextTask = 1
	}
	t.Number = p.NextTask
	p.NextTask++
	t.Ref = p.TaskRef(t.Number)
}

// withoutRefs is tasks as they are stored: a copy without derived readable
// IDs, wait pointers or prerequisite decision status.
func withoutRefs(tasks []Task) []Task {
	out := slices.Clone(tasks)
	for i := range out {
		out[i].Ref = ""
		out[i].WaitingOn = nil
		out[i].Blockers = slices.Clone(out[i].Blockers)
		for j := range out[i].Blockers {
			out[i].Blockers[j].AnswerPending = false
			out[i].Blockers[j].CurrentSettlement = ""
		}
	}
	return out
}

// backfillRefs gives every project without a prefix one, and numbers the
// tasks it has without numbers in the order they were asked for, after any
// already numbered. It runs once per project: after it, nothing is left to
// do.
func backfillRefs(v *Snapshot) {
	for i := range v.Projects {
		p := &v.Projects[i]
		if p.Prefix == "" {
			p.Prefix = uniquePrefix(v, p.Title)
		}
		var unnumbered []int
		for j, t := range v.Tasks {
			if t.ProjectID != p.ID {
				continue
			}
			if t.Number == 0 {
				unnumbered = append(unnumbered, j)
			} else if t.Number >= p.NextTask {
				p.NextTask = t.Number + 1
			}
		}
		// The list's order is the to-do order, not when a task was asked
		// for; the slice index only breaks ties.
		slices.SortStableFunc(unnumbered, func(a, b int) int {
			return v.Tasks[a].CreatedAt.Compare(v.Tasks[b].CreatedAt)
		})
		for _, j := range unnumbered {
			numberTask(p, &v.Tasks[j])
		}
		if p.NextTask < 1 {
			p.NextTask = 1
		}
	}
}

// taskByRef is the task a readable ID such as CA-12 names, in any case.
func taskByRef(v *Snapshot, ref string) *Task {
	cut := strings.LastIndexByte(ref, '-')
	if cut <= 0 {
		return nil
	}
	n, err := strconv.Atoi(ref[cut+1:])
	if err != nil || n <= 0 {
		return nil
	}
	i := slices.IndexFunc(v.Projects, func(p Project) bool { return p.Prefix != "" && strings.EqualFold(p.Prefix, ref[:cut]) })
	if i < 0 {
		return nil
	}
	for j := range v.Tasks {
		if v.Tasks[j].ProjectID == v.Projects[i].ID && v.Tasks[j].Number == n {
			return &v.Tasks[j]
		}
	}
	return nil
}

// FindTask is the task ref names: its canonical ID exactly, or its readable
// ID in any case.
func (v Snapshot) FindTask(ref string) (Task, bool) {
	if t := task(&v, strings.TrimSpace(ref)); t != nil {
		return *t, true
	}
	return Task{}, false
}

// canonicalIDs is ids with each readable ID replaced by the canonical ID
// it names; ids that name no task are left for the caller to refuse.
func canonicalIDs(v *Snapshot, ids []string) []string {
	if ids == nil {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = canonicalID(v, id)
	}
	return out
}

func canonicalID(v *Snapshot, id string) string {
	if t := task(v, strings.TrimSpace(id)); t != nil {
		return t.ID
	}
	return id
}

// MaxProjectTitle is the longest name a project can be given, in characters,
// as the dashboard's new-project form allows.
const MaxProjectTitle = 200

// SetProjectTitle renames a project. Only the title changes: its ID, task
// IDs and their prefix stay as they are, and a source refresh no longer
// overwrites the title.
func (s *Service) SetProjectTitle(ctx context.Context, id, title string) (Project, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Project{}, errors.New("a project needs a name")
	}
	if utf8.RuneCountInString(title) > MaxProjectTitle {
		return Project{}, fmt.Errorf("a project name can be at most %d characters", MaxProjectTitle)
	}
	return s.editProject(ctx, id, func(p *Project, v *Snapshot) error {
		if p.Title == title {
			return nil
		}
		old := p.Title
		p.Title = title
		p.TitleRenamed = true
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "project.renamed", fmt.Sprintf("Renamed from “%s” to “%s”", old, title))
		return nil
	})
}

// SetProjectPrefix renames a project's task ID prefix. Only the prefix is
// stored, so every task's readable ID follows, and nothing that refers to
// a task by its canonical ID changes.
func (s *Service) SetProjectPrefix(ctx context.Context, id, prefix string) (Project, error) {
	prefix, err := CleanPrefix(prefix)
	if err != nil {
		return Project{}, err
	}
	return s.editProject(ctx, id, func(p *Project, v *Snapshot) error {
		if prefixTaken(v, prefix, p.ID) {
			return fmt.Errorf("another project already uses %s: %w", prefix, ErrConflict)
		}
		if p.Prefix == prefix {
			return nil
		}
		old := p.Prefix
		p.Prefix = prefix
		p.UpdatedAt = s.now().UTC()
		record(v, p.UpdatedAt, p.ID, "project.prefix_updated", fmt.Sprintf("Task IDs now start %s- instead of %s-", prefix, old))
		return nil
	})
}
