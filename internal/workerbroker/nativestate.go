package workerbroker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// The durable side of a native worker: what it did, how full its conversation
// is, what it has consumed, and what happens to an assignment that predates all
// of this.

type diagnosticsEvent = diagnostics.Event

// maxActivity bounds what one assignment retains. Activity is a readable record
// for the owner and the assistant, not the transcript: the evidence that
// decides acceptance is the patch and the command log.
const maxActivity = 500

const evidenceCapabilityCheck = "capability_check"

// recordActivity appends one bounded, sanitized observation. Nothing private to
// the model goes in here: tool names, statuses and short excerpts only.
func (b *Broker) recordActivity(id string, entry activityEntry) {
	entry.At = now()
	entry.Detail = excerpt(entry.Detail, 512)
	_ = b.update(id, func(run *storedRun) error {
		run.Activity = append(run.Activity, entry)
		if len(run.Activity) > maxActivity {
			// Keep the most recent; the beginning of a long assignment is already
			// represented by its evidence.
			run.Activity = append([]activityEntry(nil), run.Activity[len(run.Activity)-maxActivity:]...)
			run.ActivityDropped = true
		}
		run.Run.Activity = publishedActivity(run.Activity, run.ActivityDropped)
		run.Run.UpdatedAt = now()
		return nil
	})
}

func publishedActivity(entries []activityEntry, dropped bool) []worker.Activity {
	out := make([]worker.Activity, 0, len(entries))
	for _, entry := range entries {
		out = append(out, worker.Activity{At: entry.At, Kind: entry.Kind, Tool: entry.Tool, Status: entry.Status, Detail: entry.Detail})
	}
	if dropped && len(out) > 0 {
		out[0].Truncated = true
	}
	return out
}

func excerpt(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8Valid(value[:end]) {
		end--
	}
	return value[:end] + fmt.Sprintf(" [%d more bytes omitted]", len(value)-end)
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' && len(s) > 0 {
			continue
		}
	}
	return len(strings.ToValidUTF8(s, "")) == len(s)
}

// errUncertainDelivery stops an assignment whose last prompt may or may not have
// reached its worker. It is not a failure of the worker or the provider; it is
// the daemon declining to guess.
var errUncertainDelivery = errors.New("the worker's last direction could not be confirmed delivered")

// nextTurnInput assembles what to say to the worker next: its assignment on the
// first turn, or whatever the daemon has queued since the last one.
//
// Nothing is dequeued here. Direction moves to an in-flight record carrying how
// far it got, and only a harness that actually accepted the prompt clears it.
// That record is the whole point: a prompt the daemon is certain it never sent
// can be said again, while one whose fate is unknown must not be, because the
// worker may already have acted on it. The opening brief is treated the same
// way — marking an assignment briefed before it has been told anything would
// leave a resumed worker with nothing to do and no way back.
//
// An empty result means there is genuinely nothing to say, which the caller has
// to handle rather than invent a prompt for.
func (b *Broker) nextTurnInput(id string) (string, error) {
	var prompt string
	err := b.update(id, func(run *storedRun) error {
		if run.InFlight != nil {
			if run.InFlight.Delivery == deliveryUnknown {
				return errUncertainDelivery
			}
			// Confirmed never sent. Say it again.
			prompt = run.InFlight.Text
			return nil
		}
		queued := run.Messages
		if run.Steering != nil {
			if run.Steering.Delivery == deliveryUnknown {
				return errUncertainDelivery
			}
			// A steer that was taken off the queue and never sent. It goes back at
			// the front rather than being stranded in a field nothing else reads.
			queued = append([]string{run.Steering.Text}, queued...)
			run.Steering = nil
		}
		opening := ""
		if !run.Briefed {
			// The assignment itself is in the session's appended instructions, so
			// the opening turn asks for work rather than repeating them.
			opening = "Begin this assignment. Inspect the workspace before changing it, and verify your changes with commands before reporting."
		}
		switch {
		case opening != "" && len(queued) > 0:
			prompt = opening + "\n\nThe coordinator has also sent:\n" + strings.Join(queued, "\n\n")
		case opening != "":
			prompt = opening
		case len(queued) > 0:
			prompt = "Direction from the coordinator:\n" + strings.Join(queued, "\n\n")
		default:
			return nil
		}
		run.InFlight = &inFlightInput{Text: prompt, Briefing: opening != "", Messages: len(queued), At: now(), Delivery: deliveryUnsent}
		run.Messages = nil
		return nil
	})
	return prompt, err
}

// deliveryAttempted records that the prompt is being handed over, before it is.
// Written first on purpose: a process that dies during the handover leaves this
// behind, and "sending" is exactly what recovery needs to see. Its error has to
// reach the caller — a handover that was not recorded must not happen, because
// the record is the only thing that stops it happening twice.
func (b *Broker) deliveryAttempted(id string) error {
	return b.update(id, func(run *storedRun) error {
		if run.InFlight != nil {
			run.InFlight.Delivery = deliverySending
		}
		return nil
	})
}

// deliveryAccepted records that the harness took the prompt. Until this runs the
// direction is still owed to the worker.
func (b *Broker) deliveryAccepted(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.InFlight == nil {
			return nil
		}
		if run.InFlight.Briefing {
			run.Briefed = true
		}
		if run.InFlight.Messages > 0 {
			b.recordSteeringDelivery(run, "turn_input")
		}
		run.InFlight = nil
		return nil
	})
}

// deliveryRejected records a refusal made before anything was sent. The prompt
// is owed in full and may be said again.
func (b *Broker) deliveryRejected(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.InFlight == nil {
			return nil
		}
		run.InFlight.Delivery = deliveryUnsent
		run.Run.Summary = "Direction was refused before it was sent; it will be said again"
		run.Run.UpdatedAt = now()
		return nil
	})
}

// deliveryUncertain records an attempt that established nothing. Repeating the
// prompt could duplicate work the worker already did; dropping it could lose the
// assignment. Neither is the daemon's call, so it stops and says so.
func (b *Broker) deliveryUncertain(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.InFlight == nil {
			return nil
		}
		run.InFlight.Delivery = deliveryUnknown
		return nil
	})
	b.recordActivity(id, activityEntry{Kind: "steering", Status: "uncertain", Detail: "the harness's response to this direction was never established"})
}

// reconcileDelivery runs once per run at broker startup. A record still marked
// as being sent belonged to a process that is gone, and whether the harness got
// it died with that process. Both routes into a worker are covered: the prompt
// that opens a turn and the direction handed to one already running.
func reconcileDelivery(run *storedRun) {
	for _, record := range []*inFlightInput{run.InFlight, run.Steering} {
		if record != nil && record.Delivery == deliverySending {
			record.Delivery = deliveryUnknown
		}
	}
}

// repeatPrefix marks direction the worker may already have. Saying so is the
// point: a worker told the same thing twice with no warning will do it twice.
const repeatPrefix = "The coordinator is repeating direction that may already have reached you; the daemon lost track of whether it was delivered. If you have already acted on it, say what you did rather than doing it again.\n\n"

// resolveUncertainDelivery is the owner's answer to an unknown delivery, taken
// on an explicit resume. The direction goes back to the queue marked as a
// possible repeat, so the worker can reconcile it against what it already did
// rather than being told the same thing again as if for the first time.
func (r *storedRun) resolveUncertainDelivery() {
	for _, record := range []**inFlightInput{&r.InFlight, &r.Steering} {
		current := *record
		if current == nil || current.Delivery != deliveryUnknown {
			continue
		}
		r.Messages = append([]string{repeatPrefix + current.Text}, r.Messages...)
		*record = nil
	}
}

// pendingSteer reports direction that arrived while a turn was already running,
// and takes it in flight so it is not delivered twice. Anything that arrives
// after this snapshot stays queued for the next turn, so a receipt covers only
// what was actually handed over.
func (b *Broker) pendingSteer(id string) string {
	var pending string
	_ = b.update(id, func(run *storedRun) error {
		if len(run.Messages) == 0 || run.Steering != nil {
			return nil
		}
		pending = "Direction from the coordinator:\n" + strings.Join(run.Messages, "\n\n")
		run.Steering = &inFlightInput{Text: pending, Messages: len(run.Messages), At: now(), Delivery: deliveryUnsent}
		run.Messages = nil
		return nil
	})
	return pending
}

// steerRetained records direction the daemon has decided to deliver as the next
// turn's input rather than into the running one. It is not lost and it is not
// delivered: it goes back to the queue, and the ordinary admitted loop says it.
func (b *Broker) steerRetained(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.Steering == nil {
			return nil
		}
		run.Messages = append([]string{run.Steering.Text}, run.Messages...)
		run.Steering = nil
		run.Run.Summary = "Stopping the current turn to deliver the coordinator's direction"
		run.Run.UpdatedAt = now()
		return nil
	})
	b.recordActivity(id, activityEntry{Kind: "steering", Status: "retained", Detail: "this harness cannot redirect a running turn; the turn was stopped and the direction will begin the next one"})
}

// steerAttempted records that direction is being handed to a running turn,
// before it is. Its error reaches the caller for the same reason as a turn's:
// a handover that was not recorded must not happen, because the record is what
// stops a restart from repeating it.
func (b *Broker) steerAttempted(id string) error {
	return b.update(id, func(run *storedRun) error {
		if run.Steering != nil {
			run.Steering.Delivery = deliverySending
		}
		return nil
	})
}

// steerUncertain records a handover whose outcome was never established. The
// request went out; whether the worker took it is unknown. The direction is kept
// exactly as it was, and nothing repeats it by itself.
func (b *Broker) steerUncertain(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.Steering == nil {
			return nil
		}
		run.Steering.Delivery = deliveryUnknown
		return nil
	})
	b.recordActivity(id, activityEntry{Kind: "steering", Status: "uncertain", Detail: "the running turn's response to this direction was never established"})
}

// steerDelivered records that a live turn accepted direction, and by which
// mechanism. Delivery is delivery: the worker's own acknowledgement is still the
// only thing that counts as having read it, and neither is evidence it was
// applied.
func (b *Broker) steerDelivered(id, strategy string) {
	_ = b.update(id, func(run *storedRun) error {
		run.Steering = nil
		b.recordSteeringDelivery(run, strategy)
		return nil
	})
	b.recordActivity(id, activityEntry{Kind: "steering", Status: "delivered", Detail: strategy})
}

// steerUndelivered returns direction to the queue. A steer that was refused or
// could not be acknowledged did not reach the worker, and recording a receipt
// for it would tell the coordinator their words landed when they did not.
func (b *Broker) steerUndelivered(id string) {
	_ = b.update(id, func(run *storedRun) error {
		if run.Steering == nil {
			return nil
		}
		run.Messages = append([]string{run.Steering.Text}, run.Messages...)
		run.Steering = nil
		return nil
	})
	b.recordActivity(id, activityEntry{Kind: "steering", Status: "undelivered", Detail: "direction was not accepted by the running turn and remains queued"})
}

func (b *Broker) recordSteeringDelivery(run *storedRun, strategy string) {
	run.Run.Summary = "Direction delivered to the running worker (" + strategy + ")"
	run.Run.UpdatedAt = now()
}

// overLimit reports whether what a turn has consumed so far already exceeds the
// configured budget. It is deliberately about this assignment's own ledger plus
// the turn in progress, which is the only figure available while a turn runs.
func (b *Broker) overLimit(id string, observed session.Usage) bool {
	budget := b.tokenBudget()
	if budget <= 0 || !observed.Known {
		return false
	}
	run, err := b.snapshot(id)
	if err != nil {
		return false
	}
	used := run.UsageInputTokens + run.UsageOutputTokens + inputTokens(observed) + observed.Output
	return used >= budget
}

// observeContext records how full the worker's conversation is. This is
// occupancy, not consumption, and its quality travels with it: an estimate must
// not be shown as a provider measurement.
func (b *Broker) observeContext(id string, snapshot *session.ContextSnapshot) {
	if snapshot == nil {
		return
	}
	_ = b.update(id, func(run *storedRun) error {
		run.Run.Context = worker.ContextOccupancy{
			UsedPercent: snapshot.UsedPercent,
			Quality:     string(snapshot.Quality),
			ObservedAt:  snapshot.ObservedAt,
			Invalidated: snapshot.Invalidated,
			Model:       snapshot.Model,
		}
		run.Run.UpdatedAt = now()
		return nil
	})
}

// noteCompaction counts the harness's own compactions. They are the provider's,
// not the application's: nothing here rewrites the worker's conversation.
func (b *Broker) noteCompaction(id, kind string) {
	if kind != "compaction_completed" {
		return
	}
	_ = b.update(id, func(run *storedRun) error {
		run.Run.ContextCompactions++
		run.Run.UpdatedAt = now()
		return nil
	})
}

// publishTurnObservations records what a finished turn was seen to consume,
// separately from what it is charged. A turn whose accounting the provider did
// not supply is already counted as unknown; this keeps the evidence of activity
// visible instead of leaving the owner with nothing to look at.
func (b *Broker) publishTurnObservations(id string, result session.Result, streamed session.Usage) {
	observed := result.Observed
	if !observed.Known {
		// A turn that ended without its own accounting still leaves the last
		// figure its stream reported. Keeping it is what stops an unmeasured turn
		// looking like an idle one.
		observed = streamed
	}
	if !observed.Known {
		return
	}
	_ = b.update(id, func(run *storedRun) error {
		run.Run.Usage.ObservedInputTokens += inputTokens(observed)
		run.Run.Usage.ObservedOutputTokens += observed.Output
		run.ObservedInputTokens = run.Run.Usage.ObservedInputTokens
		run.ObservedOutputTokens = run.Run.Usage.ObservedOutputTokens
		run.Run.UpdatedAt = now()
		return nil
	})
}

// legacyMigration handles an assignment created under the previous contract,
// which proposed tools through a stateless model call and has no native session
// to resume. Its workspace, baseline, artifacts, command evidence, steering
// receipts, usage ledger and authority are all still valid; only the model
// conversation is not portable.
//
// It is never resumed automatically, and its old transcript is never described
// as a native session. The owner is told exactly what carries over, and an
// explicit resume performs the handover once.
func (b *Broker) legacyMigration(id string, r storedRun) string {
	if r.Session != nil || len(r.Transcript) == 0 || r.Migrated || r.LegacyNotified {
		return ""
	}
	return "This assignment was started under the previous worker contract, which has no coding session to resume. Everything it produced is preserved: its isolated workspace, the changed files, the recorded commands, its steering receipts and its usage ledger. Its model conversation is not portable to a coding session and is kept only for inspection. Resume explicitly to continue the same assignment in a new coding session, which will be told what the previous attempt changed and verified."
}

// migrate performs the one-time handover on an explicit resume. It opens no
// session and reruns nothing: it converts the preserved evidence into a brief,
// queues it as the first thing the new session is told, and records that the
// conversion happened so it cannot repeat.
func (r *storedRun) migrate() {
	// Only after the owner has been told what this is. Converting on the first
	// resume of any assignment that happens to carry a transcript would rewrite
	// the instruction an owner had just sent, and would do it before they had
	// been told the conversation was gone.
	if r.Migrated || !r.LegacyNotified || len(r.Transcript) == 0 {
		return
	}
	r.Migrated = true
	r.Briefed = false
	r.Messages = append([]string{handoffBrief(*r)}, r.Messages...)
}

// handoffBrief describes the previous attempt from what was actually recorded —
// files and commands — not from what the old conversation claimed.
func handoffBrief(r storedRun) string {
	var b strings.Builder
	b.WriteString("This assignment was already attempted under an earlier worker contract. Its conversation is gone; its work is not. Continue it rather than starting over, and verify anything you rely on.\n")
	changed, established := changedPaths(r)
	switch {
	case !established:
		b.WriteString("\nWhat the earlier attempt changed could not be established from the preserved workspace. Inspect it before assuming anything either way; do not assume it changed nothing.")
	case len(changed) == 0:
		b.WriteString("\nComparing the preserved workspace against its baseline shows no file changes.")
	default:
		shown := changed
		if len(shown) > 24 {
			shown = append(append([]string{}, changed[:24]...), fmt.Sprintf("and %d more", len(changed)-24))
		}
		fmt.Fprintf(&b, "\nFiles changed so far (%d): %s", len(changed), strings.Join(shown, ", "))
	}
	succeeded, failed := 0, 0
	for _, command := range r.Commands {
		if command.Success {
			succeeded++
			continue
		}
		failed++
	}
	fmt.Fprintf(&b, "\nCommands recorded: %d succeeded, %d failed.", succeeded, failed)
	for i, command := range r.Commands {
		if i >= 8 {
			fmt.Fprintf(&b, "\n(%d earlier commands omitted; ask for specific output if you need it.)", len(r.Commands)-8)
			break
		}
		status := "FAILED"
		if command.Success {
			status = "succeeded"
		}
		fmt.Fprintf(&b, "\n  %s: %s", status, excerpt(command.Command, 200))
	}
	if len(r.Run.SteeringAcknowledgements) > 0 {
		fmt.Fprintf(&b, "\nDirection already acknowledged by the earlier attempt: %s. An acknowledgement is a read receipt, not evidence it was applied.", strings.Join(r.Run.SteeringAcknowledgements, ", "))
	}
	if r.Run.Usage.UnknownCalls > 0 {
		fmt.Fprintf(&b, "\n%d earlier model call(s) reported no usage, so this assignment's consumption is partly unknown.", r.Run.Usage.UnknownCalls)
	}
	b.WriteString("\nThe original project folder was never modified.")
	return b.String()
}

// changedPaths reports what the earlier attempt actually changed, by comparing
// the preserved workspace against the baseline it started from — the same
// evidence acceptance uses.
//
// It returns whether it could establish that at all. An assignment from the
// previous contract has no activity record to read, and describing that as "no
// changes" would tell the next worker the previous one did nothing when it may
// have done a great deal.
func changedPaths(r storedRun) ([]string, bool) {
	if r.WorkDir == "" || r.Baseline == nil {
		return nil, false
	}
	current := map[string]bool{}
	root, err := os.OpenRoot(r.WorkDir)
	if err != nil {
		return nil, false
	}
	defer root.Close()
	if err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." || d.IsDir() || excluded(d.Name()) || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		current[path] = true
		return nil
	}); err != nil {
		return nil, false
	}
	names := map[string]bool{}
	for name := range r.Baseline {
		names[name] = true
	}
	for name := range current {
		names[name] = true
	}
	var changed []string
	for name := range names {
		baseline, had := r.Baseline[name]
		if had != current[name] {
			changed = append(changed, name)
			continue
		}
		if !had {
			continue
		}
		raw, readErr := readConfined(root, name)
		if readErr != nil || string(raw) != string(baseline) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed, true
}

type activityEntry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Tool   string    `json:"tool,omitempty"`
	Status string    `json:"status,omitempty"`
	Detail string    `json:"detail,omitempty"`
}
