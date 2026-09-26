package work

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
)

// A turn is on show from when it is picked up to when it ends, counting its
// tool calls, edits and output as its session reports them.
func TestARunningTurnCountsWhatItDoes(t *testing.T) {
	a := testLoop(t)
	task := core.Task{ID: "task-one", ProjectID: "project-one"}
	watch := a.watchTurn(task, core.RoleImplementer, core.Role{Name: "Implementer", Member: "ada"}, t.TempDir(), false)
	if len(a.Turns()) != 0 {
		t.Fatal("a turn showed before it was picked up")
	}
	watch.Started()
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "1", Tool: "Bash"})
	watch.Saw(session.Event{Kind: "tool_completed", ItemID: "1"})
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "2", Tool: "Edit"})
	watch.Saw(session.Event{Kind: "usage", Usage: &session.Usage{Known: true, Output: 40}})
	watch.Saw(session.Event{Kind: "usage", Usage: &session.Usage{Known: true, Final: true, Output: 40}})
	got := a.Turns()
	if len(got) != 1 || got[0].TaskID != "task-one" || got[0].Member != "ada" || got[0].ToolCalls != 2 || got[0].Edits != 1 || got[0].Tool != "Edit" || got[0].OutputTokens != 40 {
		t.Fatalf("turns %+v", got)
	}
	watch.Ended()
	watch.Saw(session.Event{Kind: "tool_started", Tool: "Bash"})
	if len(a.Turns()) != 0 {
		t.Fatal("a finished turn stayed on show")
	}
}

// A turn that writes counts the files it has changed as it goes; files
// left as they were before it began don't count.
func TestAWritingTurnCountsItsChangedFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "old.md"), []byte("before"), 0o600)
	os.WriteFile(filepath.Join(dir, "kept.md"), []byte("before"), 0o600)
	before := fileTimes(dir)
	os.WriteFile(filepath.Join(dir, "new.md"), []byte("after"), 0o600)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "sub", "also.md"), []byte("after"), 0o600)
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(dir, "old.md"), later, later)
	if n := changedFiles(dir, before); n != 3 {
		t.Fatalf("changed %d", n)
	}
	a := testLoop(t)
	watch := a.watchTurn(core.Task{ID: "task-one"}, core.RoleImplementer, core.Role{}, dir, true)
	watch.Started()
	watch.Ended()
}

// A turn that runs twice, as one asked again does, counts each run afresh.
func TestARunAgainCountsAfresh(t *testing.T) {
	a := testLoop(t)
	watch := a.watchTurn(core.Task{ID: "task-one"}, core.RoleReviewer, core.Role{}, t.TempDir(), false)
	watch.Started()
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "1", Tool: "Bash"})
	watch.Ended()
	watch.Started()
	defer watch.Ended()
	if got := a.Turns(); len(got) != 1 || got[0].ToolCalls != 0 || got[0].Tool != "" {
		t.Fatalf("the second run carried the first's counts: %+v", got)
	}
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "2", Tool: "Read"})
	watch.Saw(session.Event{Kind: "tool_started", ItemID: "3", Tool: "Grep"})
	watch.Saw(session.Event{Kind: "tool_completed", ItemID: "3"})
	if got := a.Turns()[0].Tool; got != "Read" {
		t.Fatalf("running now: %s", got)
	}
}
