package work

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/media/gitrepo"
	"github.com/shhac/crew-assistant/internal/roles"
)

// countFilesEvery is how often a writing turn's changed files are counted.
// Counting reads the whole working tree, so not on every event.
const countFilesEvery = 5 * time.Second

// turnRegister holds the roles at work right now, as the owner watches
// them: who, since when, and counts that only go up while a turn is alive.
type turnRegister struct {
	mu      sync.Mutex
	running map[*liveTurn]struct{}
}

type liveTurn struct {
	reg *turnRegister
	// who is the turn as it was set up; each run starts its counts afresh
	// from it, since a turn that has to ask again runs twice.
	who  core.Turn
	turn core.Turn
	// workDir is counted for changed files while the turn writes.
	workDir string
	writes  bool
	count   func(context.Context) (int, error)
	// tools are the tools running now, in the order they started.
	tools []runningTool
	done  chan struct{}
}

type runningTool struct{ item, name string }

// has says a role is at work on the task right now.
func (r *turnRegister) has(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for t := range r.running {
		if t.turn.TaskID == taskID {
			return true
		}
	}
	return false
}

// Turns are the roles at work right now, longest-running first.
func (lp *Loop) Turns() []core.Turn {
	r := &lp.turns
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]core.Turn, 0, len(r.running))
	for t := range r.running {
		out = append(out, t.turn)
	}
	slices.SortFunc(out, func(a, b core.Turn) int { return a.StartedAt.Compare(b.StartedAt) })
	return out
}

// watchTurn is how a role's turn on t reports itself while it runs.
func (lp *Loop) watchTurn(t core.Task, kind string, r core.Role, workDir string, writes bool) roles.Observer {
	return &liveTurn{reg: &lp.turns, workDir: workDir, writes: writes, who: core.Turn{ProjectID: t.ProjectID, TaskID: t.ID, Role: kind, Seat: r.Name, Member: r.Member}}
}

func (l *liveTurn) Started() {
	if l.writes {
		l.count = counter(l.workDir)
	}
	now := time.Now().UTC()
	l.reg.mu.Lock()
	if l.reg.running == nil {
		l.reg.running = map[*liveTurn]struct{}{}
	}
	l.turn = l.who
	l.turn.StartedAt, l.turn.LastActivityAt = now, now
	l.tools, l.done = nil, make(chan struct{})
	l.reg.running[l] = struct{}{}
	l.reg.mu.Unlock()
	if l.writes {
		go l.countFiles()
	}
}

func (l *liveTurn) Saw(e session.Event) {
	l.reg.mu.Lock()
	defer l.reg.mu.Unlock()
	if _, ok := l.reg.running[l]; !ok {
		return
	}
	l.turn.LastActivityAt = time.Now().UTC()
	switch e.Kind {
	case "tool_started":
		l.turn.ToolCalls++
		if edits(e.Tool) {
			l.turn.Edits++
		}
		l.tools = append(l.tools, runningTool{e.ItemID, e.Tool})
		l.turn.Tool = e.Tool
	case "tool_completed":
		l.tools = slices.DeleteFunc(l.tools, func(t runningTool) bool { return t.item == e.ItemID })
		l.turn.Tool = ""
		if n := len(l.tools); n > 0 {
			l.turn.Tool = l.tools[n-1].name
		}
	case "usage":
		// Each model response reports its own; the turn's final figure
		// repeats them all.
		if e.Usage != nil && e.Usage.Known && !e.Usage.Final {
			l.turn.OutputTokens += e.Usage.Output
		}
	}
}

func (l *liveTurn) Ended() {
	l.reg.mu.Lock()
	defer l.reg.mu.Unlock()
	if _, ok := l.reg.running[l]; ok {
		delete(l.reg.running, l)
		close(l.done)
	}
}

// edits says whether a tool changes files, as each engine names them.
func edits(tool string) bool {
	switch tool {
	case "fileChange", "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

// countFiles counts the turn's changed files every few seconds until it
// ends.
func (l *liveTurn) countFiles() {
	tick := time.NewTicker(countFilesEvery)
	defer tick.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), countFilesEvery)
		n, err := l.count(ctx)
		cancel()
		if err == nil {
			l.reg.mu.Lock()
			l.turn.FilesChanged = &n
			l.reg.mu.Unlock()
		}
		select {
		case <-l.done:
			return
		case <-tick.C:
		}
	}
}

// counter counts how many files in dir differ from where a turn began: what
// git says has changed in a repository's working tree, or otherwise the
// files new or rewritten since.
func counter(dir string) func(context.Context) (int, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return func(ctx context.Context) (int, error) { return gitrepo.ChangedFiles(ctx, dir) }
	}
	before := fileTimes(dir)
	return func(context.Context) (int, error) { return changedFiles(dir, before), nil }
}

// changedFiles is how many files in dir are new or rewritten since before.
// File times are compared with themselves rather than the clock, which a
// filesystem may stamp coarsely enough to put a fresh write before the turn
// began.
func changedFiles(dir string, before map[string]time.Time) int {
	n := 0
	for path, at := range fileTimes(dir) {
		if was, ok := before[path]; !ok || !at.Equal(was) {
			n++
		}
	}
	return n
}

// fileTimes is when each file under dir was last written.
func fileTimes(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out[path] = info.ModTime()
		}
		return nil
	})
	return out
}
