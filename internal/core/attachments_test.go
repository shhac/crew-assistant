package core

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pngBytes is a small, real PNG.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// keptFiles lists the files kept for a task, if any.
func keptFiles(t *testing.T, s *Service, taskID string) []string {
	t.Helper()
	entries, err := os.ReadDir(s.AttachmentsDirectory(taskID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestTheOwnersNoteKeepsItsFilesSafelyAndTheyAreFoundOnlyOnTheirTask(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	other, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Another"})
	shot := pngBytes(t)
	note, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: queued.ID, By: FromOwner, Kind: FromOwner, Text: "The layout I mean", Files: []NewFile{{Name: "layout.png", Data: shot}, {Name: "copy.md", Data: []byte("# Heading\n")}}})
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(queued.ID)
	if len(task.Notes) != 1 || len(task.Attachments) != 2 {
		t.Fatalf("notes %+v attachments %+v", task.Notes, task.Attachments)
	}
	for _, a := range task.Attachments {
		if a.Note != note.ID || a.Design != "" || a.By != FromOwner || a.Kind != FromOwner || a.At.IsZero() || !attachmentID.MatchString(a.ID) {
			t.Fatalf("attachment %+v", a)
		}
	}
	if a := task.Attachments[0]; a.Name != "layout.png" || a.Type != "image/png" || a.Size != int64(len(shot)) {
		t.Fatalf("image %+v", a)
	}
	if a := task.Attachments[1]; a.Type != "text/markdown" || !a.IsText() {
		t.Fatalf("text %+v", a)
	}
	a, path, err := s.OpenAttachment(testContext, p.ID, task.Ref, task.Attachments[0].ID)
	if err != nil || a.Name != "layout.png" {
		t.Fatalf("found %+v %v", a, err)
	}
	if kept, err := os.ReadFile(path); err != nil || !bytes.Equal(kept, shot) {
		t.Fatalf("the file kept is not the file sent: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", info.Mode())
	}
	if info, _ := os.Stat(filepath.Dir(path)); info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", info.Mode())
	}
	if filepath.Dir(path) != s.AttachmentsDirectory(queued.ID) || strings.Contains(path, "layout") {
		t.Fatalf("kept at %s", path)
	}
	for name, lookup := range map[string][3]string{
		"another task":    {p.ID, other.ID, task.Attachments[0].ID},
		"another project": {"elsewhere", queued.ID, task.Attachments[0].ID},
		"a made-up id":    {p.ID, queued.ID, strings.Repeat("0", 24)},
		"a path":          {p.ID, queued.ID, "../../state.db"},
	} {
		if _, _, err := s.OpenAttachment(testContext, lookup[0], lookup[1], lookup[2]); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A note can be files alone.
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: queued.ID, By: FromOwner, Kind: FromOwner, Files: []NewFile{{Name: "more.txt", Data: []byte("more")}}}); err != nil {
		t.Fatalf("a note of files alone: %v", err)
	}
}

func TestAttachmentsAreJudgedByTheirBytesAndASetIsNeverPartlyKept(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	good := NewFile{Name: "fine.md", Data: []byte("fine")}
	for name, c := range map[string]struct {
		file NewFile
		says string
	}{
		"text posing as a PNG": {NewFile{Name: "mock.png", Data: []byte("not a picture")}, "mock.png can't be attached: it is not a PNG image"},
		"a PNG posing as JPEG": {NewFile{Name: "mock.jpg", Data: pngBytes(t)}, "it is not a JPEG image"},
		"text with a NUL":      {NewFile{Name: "notes.txt", Data: []byte("a\x00b")}, "notes.txt can't be attached: it contains binary data"},
		"not UTF-8":            {NewFile{Name: "notes.txt", Data: []byte{0xff, 0xfe, 'a'}}, "it is not UTF-8 text"},
		"a PDF that isn't":     {NewFile{Name: "spec.pdf", Data: []byte("hello")}, "it is not a PDF"},
		"a program":            {NewFile{Name: "run.sh", Data: []byte("echo hi")}, "only images (PNG, JPEG, GIF, WebP), PDF, and text files"},
		"no extension":         {NewFile{Name: "README", Data: []byte("hi")}, "only images"},
		"empty":                {NewFile{Name: "empty.md", Data: nil}, "empty.md can't be attached: it is empty"},
		"too large":            {NewFile{Name: "big.txt", Data: bytes.Repeat([]byte("a"), MaxAttachmentBytes+1)}, "it is 5,242,881 bytes, and a file can be at most 5,242,880 bytes"},
		"a path for a name":    {NewFile{Name: "../../state.db.md", Data: []byte("x")}, "a name can't be a path or start with a dot"},
		"a backslashed path":   {NewFile{Name: `..\up.md`, Data: []byte("x")}, "a name can't be a path"},
		"a hidden name":        {NewFile{Name: ".env.txt", Data: []byte("x")}, "start with a dot"},
		"a control character":  {NewFile{Name: "a\nb.md", Data: []byte("x")}, "characters a name can't hold"},
		"no name":              {NewFile{Name: "  ", Data: []byte("x")}, "an attachment needs a name"},
		"an overlong name":     {NewFile{Name: strings.Repeat("a", 130) + ".md", Data: []byte("x")}, "a name can be at most 120 bytes"},
	} {
		_, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: queued.ID, By: FromOwner, Kind: FromOwner, Text: "See these", Files: []NewFile{good, c.file}})
		if err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	eleven := make([]NewFile, MaxAttachmentsPerSet+1)
	for i := range eleven {
		eleven[i] = good
	}
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: queued.ID, By: FromOwner, Kind: FromOwner, Files: eleven}); err == nil || !strings.Contains(err.Error(), "the most is 10") {
		t.Fatalf("eleven files at once: %v", err)
	}
	// A set refused once its files were written, here because the team no
	// longer changes a task that moved on, leaves nothing behind.
	if _, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: queued.ID, By: "Rune", Kind: RoleReviewer, While: TaskReviewing, Text: "Late", Files: []NewFile{good}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a note for a task that moved on: %v", err)
	}
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(queued.ID)
	if len(task.Notes) != 0 || len(task.Attachments) != 0 || len(keptFiles(t, s, queued.ID)) != 0 {
		t.Fatalf("a refused set left notes %d, records %d, files %v", len(task.Notes), len(task.Attachments), keptFiles(t, s, queued.ID))
	}
	// Every other kind named is taken.
	for _, f := range []NewFile{{Name: "a.svg", Data: []byte("<svg/>")}, {Name: "a.html", Data: []byte("<p>")}, {Name: "a.json", Data: []byte("{}")}, {Name: "a.csv", Data: []byte("a,b")}, {Name: "a.PDF", Data: []byte("%PDF-1.7")}, {Name: "a.png", Data: pngBytes(t)}} {
		if _, err := checkFile(f); err != nil {
			t.Errorf("%s: %v", f.Name, err)
		}
	}
}

func TestATaskKeepsAttachmentsOnlyUpToItsLimits(t *testing.T) {
	s, _ := fixture(t)
	p := newProject(t, s)
	add := func(taskID string, n, size int) error {
		files := make([]NewFile, n)
		for i := range files {
			files[i] = NewFile{Name: "part.txt", Data: bytes.Repeat([]byte("a"), size)}
		}
		_, err := s.AddNote(testContext, NoteInput{Project: p.ID, Task: taskID, By: FromOwner, Kind: FromOwner, Files: files})
		return err
	}
	counted, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Many files"})
	for range MaxTaskAttachments / MaxAttachmentsPerSet {
		if err := add(counted.ID, MaxAttachmentsPerSet, 10); err != nil {
			t.Fatal(err)
		}
	}
	if err := add(counted.ID, 1, 10); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d attachments, and this one has %d", MaxTaskAttachments, MaxTaskAttachments)) {
		t.Fatalf("one file past the limit: %v", err)
	}
	if n := len(keptFiles(t, s, counted.ID)); n != MaxTaskAttachments {
		t.Fatalf("%d files kept", n)
	}
	sized, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Large files"})
	sets := MaxTaskAttachmentBytes / (MaxAttachmentsPerSet * MaxAttachmentBytes)
	for range sets {
		if err := add(sized.ID, MaxAttachmentsPerSet, MaxAttachmentBytes); err != nil {
			t.Fatalf("up to the total limit: %v", err)
		}
	}
	if err := add(sized.ID, 1, 1); err == nil || !strings.Contains(err.Error(), "at most "+ExactBytes(MaxTaskAttachmentBytes)+" of attachments") {
		t.Fatalf("past the total limit: %v", err)
	}
	if n := len(keptFiles(t, s, sized.ID)); n != sets*MaxAttachmentsPerSet {
		t.Fatalf("%d files kept", n)
	}
}

func TestTheDesignerAttachesOnlyToTheInputItIsGiving(t *testing.T) {
	s, _ := fixture(t)
	p := designedProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	s.NextTask(testContext)
	files := DesignFiles{Project: p.ID, Task: queued.ID, By: "Dee", Kind: RoleDesigner, Files: []NewFile{{Name: "card.svg", Data: []byte("<svg/>")}}}
	if _, err := s.AttachToDesign(testContext, files); !errors.Is(err, ErrConflict) {
		t.Fatalf("attached with no design asked for: %v", err)
	}
	held, _ := s.AskDesign(testContext, queued.ID, DesignAsk{From: "Researcher", Question: "Cards or rows?", Owner: designOwner})
	files.Design, files.While = held.Design[0].ID, TaskDesigning
	kept, err := s.AttachToDesign(testContext, files)
	if err != nil || len(kept) != 1 || kept[0].Design != held.Design[0].ID || kept[0].Note != "" || kept[0].By != "Dee" {
		t.Fatalf("attached %+v %v", kept, err)
	}
	s.RecordDesign(testContext, queued.ID, held.Design[0].ID, DesignReply{Designer: "Dee", Input: "Cards", Current: CurrentThis})
	if _, err := s.AttachToDesign(testContext, files); !errors.Is(err, ErrConflict) {
		t.Fatalf("attached to input already given: %v", err)
	}
	if n := len(keptFiles(t, s, queued.ID)); n != 1 {
		t.Fatalf("%d files kept", n)
	}
}

var designOwner = DecisionInput{Title: "More design input?", Context: "Cards or rows?", Recommendation: "Answer it", Choices: []string{"Use your judgment", "Stop"}}

// designedProject is a planned project whose team has Dee as its designer.
func designedProject(t *testing.T, s *Service) Project {
	t.Helper()
	p := plannedProject(t, s)
	playbook := *p.Playbook
	playbook.Roles = append(playbook.Roles, Role{Name: "Dee", Kinds: []string{RoleDesigner}, Engine: "claude"})
	p, err := s.SetPlaybook(testContext, p.ID, playbook)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheDesignerMarksOneDesignCurrentAndEarlierOnesStayAsSuperseded(t *testing.T) {
	s, _ := fixture(t)
	p := designedProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	s.NextTask(testContext)
	answer := func(reply DesignReply) (Task, error) {
		t.Helper()
		held, err := s.AskDesign(testContext, queued.ID, DesignAsk{From: "Researcher", Question: "Cards or rows?", Owner: designOwner})
		if err != nil || held.Status != TaskDesigning {
			t.Fatalf("ask: %s %v", held.Status, err)
		}
		reply.Designer = "Dee"
		return s.RecordDesign(testContext, queued.ID, held.OpenDesign().ID, reply)
	}
	first, err := answer(DesignReply{Input: "Cards", Current: CurrentThis})
	if err != nil || first.Design[0].N != 1 || !first.Design[0].Marked || first.CurrentDesign != first.Design[0].ID {
		t.Fatalf("the first design is current: %+v %v", first.Design, err)
	}
	advised, err := answer(DesignReply{Input: "Use the brand blue"})
	if err != nil || advised.Design[1].N != 2 || advised.Design[1].Marked || advised.CurrentDesign != first.Design[0].ID {
		t.Fatalf("advice leaves the current design as it was: %+v %q %v", advised.Design, advised.CurrentDesign, err)
	}
	// The researcher has asked all it may this round; the implementer asks
	// the rest.
	if _, err := s.UpdateTask(testContext, queued.ID, func(t *Task, _ *Project) (string, error) {
		t.Status = TaskWriting
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := answer(DesignReply{Input: "Rows after all", Current: CurrentThis})
	current, ok := second.CurrentDesignInput()
	if err != nil || !ok || current.N != 3 || !second.Design[0].Marked || second.Design[1].Marked {
		t.Fatalf("the new design is current and the first superseded: %+v %v", second.Design, err)
	}
	restored, err := answer(DesignReply{Input: "Back to cards", Current: 1})
	current, _ = restored.CurrentDesignInput()
	if err != nil || current.N != 1 || restored.Design[3].N != 4 || restored.Design[3].Marked {
		t.Fatalf("design 1 is brought back: %+v %v", restored.Design, err)
	}
	snap, _ := s.Snapshot(testContext)
	var marked []string
	for _, a := range snap.Activity {
		if a.Kind == "task.design_current" {
			marked = append(marked, a.Summary)
		}
	}
	// Every change of the current design is recorded, and advice records
	// none. Entries made at the same moment have no order to check.
	slices.Sort(marked)
	if strings.Join(marked, "|") != "Dee marked design 1 current on A draft|Dee marked design 1 current on A draft|Dee marked design 3 current on A draft" {
		t.Fatalf("recorded %q", marked)
	}
}

func TestADesignThatDoesNotExistIsNeverMadeCurrent(t *testing.T) {
	s, _ := fixture(t)
	p := designedProject(t, s)
	queued, _ := s.QueueTask(testContext, p.ID, TaskInput{Objective: "A draft"})
	s.NextTask(testContext)
	held, _ := s.AskDesign(testContext, queued.ID, DesignAsk{From: "Researcher", Question: "Cards or rows?", Owner: designOwner})
	id := held.Design[0].ID
	if _, err := s.RecordDesign(testContext, queued.ID, id, DesignReply{Designer: "Dee", Input: "Cards", Current: 7}); err == nil || !strings.Contains(err.Error(), "no design 7") {
		t.Fatalf("design 7: %v", err)
	}
	if _, err := s.RecordDesign(testContext, queued.ID, id, DesignReply{Designer: "Dee", Current: CurrentThis}); err == nil {
		t.Fatal("empty input was made current")
	}
	snap, _ := s.Snapshot(testContext)
	task, _ := snap.FindTask(queued.ID)
	if task.Status != TaskDesigning || task.CurrentDesign != "" || task.Design[0].N != 0 || !task.Design[0].Open() {
		t.Fatalf("a refused answer changed the task: %s %+v", task.Status, task.Design)
	}
}
