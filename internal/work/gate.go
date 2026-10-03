package work

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// interactiveGrace is how long after the owner's own use no new role turn
// starts, so a quick follow-up doesn't queue behind one.
const interactiveGrace = 5 * time.Second

// gate counts the role turns running on each engine, across every project,
// and holds them within the engine's safety cap when the owner has set one;
// it starts none while the owner's own use is in flight: a chat message
// queued or being answered, a composer request. That use never takes a slot
// or waits on one; turns already running go on.
type gate struct {
	mu          sync.Mutex
	running     map[string]int
	interactive int
	// chat is a chat message queued or being answered, as the record last
	// said; it clears only once a look at the record finds none.
	chat      bool
	quietFrom time.Time
	// freed is closed, and replaced, whenever a slot may have come free or
	// the owner's use may have ended.
	freed chan struct{}
}

// busy reports the owner's own use in flight, or just ended; the caller
// holds mu.
func (g *gate) busy() bool {
	return g.interactive > 0 || g.chat || time.Now().Before(g.quietFrom)
}

// admit takes a slot on engine for a role turn, if one is free and the
// owner isn't busy.
func (lp *Loop) admit(engine string) bool { return lp.take(engine, false) }

// take takes a slot on engine for a role turn, if one is free.
func (lp *Loop) take(engine string, forOwner bool) bool { return lp.try(engine, forOwner) == "" }

// try takes a slot on engine for a role turn, if one is free: for the
// owner's own ask, such as the assistant's question to the PM in their
// chat, whatever else the owner is doing, and otherwise only while they
// aren't busy. An engine with no safety cap always has one free. It returns
// "" when it took one, or else what the turn waits for.
func (lp *Loop) try(engine string, forOwner bool) string {
	g := &lp.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	if !forOwner && g.busy() {
		return core.WaitOwner
	}
	if g.running == nil {
		g.running = map[string]int{}
	}
	if limit := lp.Config().Engines.RoleRuns(engine); limit > 0 && g.running[engine] >= limit {
		return core.WaitEngineCap
	}
	g.running[engine]++
	return ""
}

// free gives back a slot admit took.
func (lp *Loop) free(engine string) {
	g := &lp.gate
	g.mu.Lock()
	g.running[engine]--
	g.signal()
	g.mu.Unlock()
	lp.Nudge()
}

// signal wakes whoever waits for a slot; the caller holds mu.
func (g *gate) signal() {
	if g.freed != nil {
		close(g.freed)
		g.freed = nil
	}
}

// waitAdmit waits for a slot on engine, for a turn a step runs that it did
// not claim a seat for, such as the PM's choice while deciding.
func (lp *Loop) waitAdmit(ctx context.Context, engine string) error {
	for !lp.admit(engine) {
		g := &lp.gate
		g.mu.Lock()
		if g.freed == nil {
			g.freed = make(chan struct{})
		}
		freed := g.freed
		g.mu.Unlock()
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-freed:
		case <-timer.C:
		}
		timer.Stop()
	}
	return nil
}

// Interactive marks the owner's own use as in flight until done is called:
// no new role turn starts meanwhile, nor for a moment after.
func (lp *Loop) Interactive() (done func()) {
	g := &lp.gate
	g.mu.Lock()
	g.interactive++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.interactive--
			g.quietFrom = time.Now().Add(interactiveGrace)
			g.signal()
			g.mu.Unlock()
			time.AfterFunc(interactiveGrace, lp.wake)
		})
	}
}

// NoteInteractive holds new role turns back from when a chat message is
// queued until a look at the record finds no message queued or being
// answered, and for a moment after.
func (lp *Loop) NoteInteractive() {
	lp.noteChat(true)
	lp.Nudge()
}

// noteChat records whether the record has a chat message queued or being
// answered. The moment after the last one clears still holds new turns.
func (lp *Loop) noteChat(pending bool) {
	g := &lp.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case pending:
		g.chat = true
	case g.chat:
		g.chat = false
		g.quietFrom = time.Now().Add(interactiveGrace)
		time.AfterFunc(interactiveGrace, lp.wake)
	}
}

// chatPending reports a chat message queued or being answered.
func chatPending(snap core.Snapshot) bool {
	return slices.ContainsFunc(snap.ChatTurns, func(t core.ChatTurn) bool { return t.Status == "queued" || t.Status == "running" })
}

// wake looks again at the work, and has turns waiting for the owner to
// finish look again too.
func (lp *Loop) wake() {
	lp.gate.mu.Lock()
	lp.gate.signal()
	lp.gate.mu.Unlock()
	lp.Nudge()
}

// waitQuiet waits, as a turn is about to start, until the owner's own use
// is not in flight: checked under the gate's lock at the moment the turn
// starts, so a turn claimed just before a chat or composer request began
// starts only once it is over.
func (lp *Loop) waitQuiet(ctx context.Context) error {
	g := &lp.gate
	for {
		// The record says whether chat is still queued or answered.
		if snap, err := lp.Core.Snapshot(ctx); err == nil {
			lp.noteChat(chatPending(snap))
		}
		g.mu.Lock()
		if !g.busy() {
			g.mu.Unlock()
			return nil
		}
		if g.freed == nil {
			g.freed = make(chan struct{})
		}
		freed := g.freed
		g.mu.Unlock()
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-freed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// slots are the engine slots one change took for the seats it claims.
type slots struct {
	lp    *Loop
	taken []string
	// forOwner takes slots for the owner's own ask; see take.
	forOwner bool
}

func (s *slots) admit(r core.Role) string {
	if why := s.lp.try(r.Engine, s.forOwner); why != "" {
		return why
	}
	s.taken = append(s.taken, r.Engine)
	return ""
}

// giveBack frees the slots of a change that was not recorded.
func (s *slots) giveBack() {
	for _, engine := range s.taken {
		s.lp.free(engine)
	}
	s.taken = nil
}

type slotKey struct{}

// ownerAskedKey marks a turn the owner's own chat asked for.
type ownerAskedKey struct{}

// fencedTools answers a turn's tool calls under the claim the turn holds.
type fencedTools struct {
	session.ToolHandler
	turn context.Context
}

func (f fencedTools) CallTool(ctx context.Context, call session.ToolCall) (session.ToolResult, error) {
	return f.ToolHandler.CallTool(core.FencedLike(ctx, f.turn), call)
}

// runRole runs a role's turn within its engine's safety cap, if one is set:
// in the slot its step claimed, or else once one is free. A turn for a
// claim records its launch where a restart looks for it.
func (lp *Loop) runRole(ctx context.Context, spec roles.Spec) (roles.Result, error) {
	if held, _ := ctx.Value(slotKey{}).(string); held != spec.Engine {
		if err := lp.waitAdmit(ctx, spec.Engine); err != nil {
			return roles.Result{}, err
		}
		defer lp.free(spec.Engine)
	}
	if _, token, ok := core.FenceOf(ctx); ok {
		if spec.LaunchDir == "" {
			spec.LaunchDir = lp.launchDir(token)
		}
		// What the role changes through its tools is fenced by its claim
		// too, so a turn that was stopped, or outlived a restart, changes
		// nothing through them.
		if spec.Handler != nil {
			spec.Handler = fencedTools{spec.Handler, ctx}
		}
	}
	// A step claimed before the owner began chatting or composing starts
	// its turn only once they are done; one the owner's chat asked for is
	// part of it.
	if asked, _ := ctx.Value(ownerAskedKey{}).(bool); !asked {
		if err := lp.waitQuiet(ctx); err != nil {
			return roles.Result{}, err
		}
	}
	if spec.Browser && spec.Engine == "codex" && spec.BridgeHome == "" {
		spec.BridgeHome = lp.Config().Engines.BridgeHome(spec.Engine)
	}
	result, err := lp.runner.Run(ctx, spec)
	reason, unusable := roles.BrowserUnusable(err, spec.BridgeHome)
	if err == nil || !spec.Browser || !unusable {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	// An unavailable browser or bridge does not stop the task. The retry
	// keeps every sandbox setting and drops only browser access.
	spec.Browser = false
	spec.BridgeHome = ""
	spec.Observer = browserFallbackObserver{Observer: spec.Observer, note: "Ran without the browser. " + reason}
	spec.Instructions = strings.TrimSpace(spec.Instructions + "\n\n" + noBrowserNote + " " + reason)
	return lp.runner.Run(ctx, spec)
}

// noBrowserNote takes back the browser a turn was told it had.
const noBrowserNote = "The browser couldn't be reached this turn, so you have none, whatever was said above; do the work without it."

// Record only when the fallback starts, never on the refused attempt.
type browserFallbackObserver struct {
	roles.Observer
	note string
}

func (o browserFallbackObserver) Started() {
	if o.Observer != nil {
		o.Observer.Started()
		if n, ok := o.Observer.(interface{ Note(string) }); ok {
			n.Note(o.note)
		}
	}
}
func (o browserFallbackObserver) Asked(s string) {
	if o.Observer != nil {
		o.Observer.Asked(s)
	}
}
func (o browserFallbackObserver) Saw(e session.Event) {
	if o.Observer != nil {
		o.Observer.Saw(e)
	}
}
func (o browserFallbackObserver) Ended() {
	if o.Observer != nil {
		o.Observer.Ended()
	}
}
