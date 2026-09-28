package work

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
)

// keepStreamingEvery is how often a reply still streaming in is kept, so the
// owner can watch it grow without every fragment being written.
const keepStreamingEvery = time.Second

// stepLog keeps what a turn on a task does as steps the owner can read back
// in the member's panel: the prompt it was given, the replies it writes, and
// the tools it runs with what went in and came out. They are kept for the
// owner alone; nothing here reaches a prompt or a log.
type stepLog struct {
	mu   sync.Mutex
	keep func(core.TurnStep)
	// who is the turn's task and seat; run names this run of it, since a
	// turn that has to ask again runs twice.
	who core.TurnStep
	run string
	// open are the steps still changing, by item: a reply streaming in, a
	// tool not yet finished. None are open between runs.
	open  map[string]*openStep
	items int
}

type openStep struct {
	core.TurnStep
	// changed says it has changed since it was last kept, at kept.
	changed bool
	kept    time.Time
}

// keepStep records a step. A step that can't be kept is lost, not retried:
// the turn it belongs to goes on either way.
func (lp *Loop) keepStep(step core.TurnStep) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lp.Core.RecordTurnStep(ctx, step); err != nil {
		lp.Diagnostics.Failure(diagnostics.Event{Component: "daemon", Stage: "turn_steps"}, err)
	}
}

func (s *stepLog) start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b [8]byte
	rand.Read(b[:])
	s.run, s.open, s.items = hex.EncodeToString(b[:]), map[string]*openStep{}, 0
}

func (s *stepLog) asked(prompt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		return
	}
	step := s.opened("prompt", core.StepPrompt)
	step.Text = prompt
	s.write(step, true)
	delete(s.open, "prompt")
}

// saw turns a session's event into the step it adds to or changes.
func (s *stepLog) saw(e session.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		return
	}
	switch e.Kind {
	case "text_delta", "text":
		// A text event replaces what its item had streamed so far.
		step := s.opened(e.ItemID, core.StepReply)
		if e.Kind == "text" {
			step.Text = e.Text
		} else {
			step.Text += e.Text
		}
		s.write(step, false)
	case "tool_started":
		s.settle()
		step := s.opened(e.ItemID, core.StepTool)
		step.Tool, step.Status, step.Input = e.Tool, "running", string(e.Input)
		step.Clipped = step.Clipped || e.InputTruncated
		s.write(step, true)
	case "tool_completed":
		s.settle()
		// A tool the engine runs itself is reported once, as it finishes.
		step := s.opened(e.ItemID, core.StepTool)
		step.Tool = cmp.Or(step.Tool, e.Tool)
		if step.Input == "" && len(e.Input) > 0 {
			step.Input, step.Clipped = string(e.Input), step.Clipped || e.InputTruncated
		}
		step.Status, step.Output, step.ExitCode = cmp.Or(e.Status, "completed"), e.Output, e.ExitCode
		step.Clipped = step.Clipped || e.OutputTruncated
		s.write(step, true)
		delete(s.open, step.Item)
	}
}

// end keeps what was still streaming, and marks the tools the turn never saw
// finish as stopped with their outcome unknown.
func (s *stepLog) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		return
	}
	s.settle()
	for _, step := range s.open {
		if step.Kind == core.StepTool && step.Status == "running" {
			step.Status, step.changed = "interrupted", true
			s.write(step, true)
		}
	}
	s.open = nil
}

// opened is the open step for item, begun now if there isn't one. A step the
// session gave no item is its own.
func (s *stepLog) opened(item, kind string) *openStep {
	s.items++
	if item == "" {
		item = fmt.Sprintf("step-%d", s.items)
	}
	step, ok := s.open[item]
	if !ok {
		step = &openStep{TurnStep: s.who}
		step.Turn, step.Item, step.Kind, step.At = s.run, item, kind, time.Now().UTC()
		s.open[item] = step
	}
	step.changed = true
	return step
}

// settle keeps every reply as it finally stands and closes it: another step
// has begun, so its stream is over.
func (s *stepLog) settle() {
	for item, step := range s.open {
		if step.Kind == core.StepReply {
			s.write(step, true)
			delete(s.open, item)
		}
	}
}

// write keeps a step that has changed: now, or once it has been a while
// since it was last kept, which a reply streaming in waits for.
func (s *stepLog) write(step *openStep, now bool) {
	if !step.changed || !now && !step.kept.IsZero() && time.Since(step.kept) < keepStreamingEvery {
		return
	}
	if s.who.TaskID != "" {
		s.keep(step.TurnStep)
	}
	step.changed, step.kept = false, time.Now()
}
