package workerbroker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/engine"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// Admission is now the only per-turn gate. The previous contract counted model
// calls and capped them; a native worker's turn is many provider requests, so
// what is authorized here is a turn and what is measured is what the harness
// reports for it. These tests drive that gate directly, because the engine
// transport it sits in front of is covered end-to-end elsewhere.

func startedRun(t *testing.T, b *Broker) string {
	t.Helper()
	response := request(t, b, "/runs", "dispatch-one", startRequest())
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	var run worker.Run
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// runningRun is an assignment the daemon has already dispatched, which is the
// state a turn is admitted from.
func runningRun(t *testing.T, b *Broker) string {
	t.Helper()
	id := startedRun(t, b)
	if err := b.update(id, func(r *storedRun) error {
		r.Run.Status = "running"
		r.Container = "agent-assistant-" + id
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func reserve(t *testing.T, b *Broker, id string) string {
	t.Helper()
	request := ""
	if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); err != nil {
		t.Fatal(err)
	}
	if request == "" {
		t.Fatal("admission produced no reservation")
	}
	return request
}

func engineUsage(input, output int) engine.Usage {
	return engine.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output, Known: true}
}

// The former ceiling was 16 cumulative calls, and it counted successful
// thinking. Useful work must be able to run well past it.
func TestWorkerRunsPastTheFormerCallCeiling(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	for i := 0; i < 64; i++ {
		request := ""
		if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); err != nil {
			t.Fatalf("turn %d was refused: %v", i+1, err)
		}
		if err := b.settleUsage(id, request, engineUsage(10, 5)); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := b.snapshot(id)
	if r.Run.Status != "running" || r.Run.ResourceHold != nil {
		t.Fatalf("a long assignment was stopped by its own length: %q %+v", r.Run.Status, r.Run.ResourceHold)
	}
	if r.UsageInputTokens != 640 || r.UsageOutputTokens != 320 || r.UsageUnknownCalls != 0 {
		t.Fatalf("ledger disagrees with the turns taken: %+v", r.Run.Usage)
	}
}

// Every turn passes the injected gate, and a refusal spends nothing.
func TestAdmissionCoversEveryTurnAndRefusalSpendsNothing(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	admitted := 0
	b.cfg.Admit = func(context.Context) error {
		admitted++
		return &worker.HoldError{Hold: worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, Reason: "Account allowance consumed; resets soon", ResetsAt: time.Now().Add(time.Hour).UTC()}}
	}
	id := runningRun(t, b)
	before, _ := b.snapshot(id)
	request := ""
	if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); err == nil {
		t.Fatal("held turn proceeded")
	}
	after, _ := b.snapshot(id)
	if admitted != 1 || request != "" {
		t.Fatalf("gate ran %d times and produced reservation %q", admitted, request)
	}
	if after.ModelCalls != before.ModelCalls || after.PendingUsage != nil || after.UsageUnknownCalls != 0 {
		t.Fatal("refused turn was accounted for")
	}
	if after.Run.ResourceHold == nil || after.Run.ResourceHold.OwnerAction || after.Run.ResourceHold.ResetsAt.IsZero() {
		t.Fatalf("quota hold did not record a self-clearing wait: %+v", after.Run.ResourceHold)
	}
	if after.Run.ProviderFailures != 0 || after.Run.ProviderFailureKind != "" || !after.Run.RetryAt.IsZero() {
		t.Fatal("hold masqueraded as a provider failure")
	}
}

// A held run keeps the owner's queued direction for the continuation rather than
// answering it with a refusal, and will not accept new direction while held.
func TestResourceHoldPreservesQueuedOwnerDirection(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	if err := b.update(id, func(r *storedRun) error {
		r.Messages = append(r.Messages, "Prefer the smaller change")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.cfg.Admit = func(context.Context) error {
		return &worker.HoldError{Hold: worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, Reason: "Account allowance consumed"}}
	}
	reservation := ""
	if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &reservation); err == nil {
		t.Fatal("held turn proceeded")
	}
	b.finalize(id, "usage_wait", "Account allowance consumed", nil)
	held, _ := b.snapshot(id)
	if held.Run.Status != "usage_wait" || held.Run.ResourceHold == nil {
		t.Fatalf("hold published without a wait: %q %+v", held.Run.Status, held.Run.ResourceHold)
	}
	if len(held.Messages) != 1 || held.Messages[0] != "Prefer the smaller change" {
		t.Fatalf("owner direction was lost across the hold: %v", held.Messages)
	}
	if code := request(t, b, "/runs/"+id+"/messages", "direction-one", map[string]string{"message": "More"}).Code; code != 409 {
		t.Fatal("a held worker accepted a message instead of requiring resume", code)
	}
	// The hold clears on its own, and an explicit resume continues the same
	// assignment with everything it had.
	b.cfg.Admit = nil
	if code := request(t, b, "/runs/"+id+"/resume", "resume-one", map[string]string{"instruction": "Continue"}).Code; code != 200 {
		t.Fatal("resume refused after the hold cleared", code)
	}
	resumed, _ := b.snapshot(id)
	if resumed.Run.ResourceHold != nil {
		t.Fatal("a stale hold survived the resume")
	}
	if len(resumed.Messages) < 2 {
		t.Fatalf("resume discarded queued direction: %v", resumed.Messages)
	}
}

// A configured token budget stops the next turn, and raising it lets the same
// assignment continue with its saved session.
func TestTokenBudgetStopsAndExtendsWithoutLosingTheSession(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	budget := int64(1000)
	b.cfg.TokenBudget = func() int64 { return budget }
	id := runningRun(t, b)
	if err := b.update(id, func(r *storedRun) error {
		r.Session = &session.Ref{Engine: "claude", ID: "session-1"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for {
		request := ""
		if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); err != nil {
			break
		}
		if err := b.settleUsage(id, request, engineUsage(300, 150)); err != nil {
			t.Fatal(err)
		}
	}
	held, _ := b.snapshot(id)
	if held.Run.ResourceHold == nil || held.Run.ResourceHold.Kind != worker.HoldTokenBudget || !held.Run.ResourceHold.OwnerAction {
		t.Fatalf("budget hold is not an owner decision: %+v", held.Run.ResourceHold)
	}
	used := held.UsageInputTokens + held.UsageOutputTokens
	if used < budget {
		t.Fatal("stopped before the budget was reached", used)
	}
	if held.Session == nil {
		t.Fatal("the budget hold discarded the coding session")
	}
	if !strings.Contains(held.Run.ResourceHold.Reason, "resume this assignment explicitly") {
		t.Errorf("the hold did not say how to continue: %q", held.Run.ResourceHold.Reason)
	}
	budget = 100000
	after := reserve(t, b, id)
	if err := b.settleUsage(id, after, engineUsage(1, 1)); err != nil {
		t.Fatal(err)
	}
	continued, _ := b.snapshot(id)
	if continued.Session == nil || continued.Session.ID != "session-1" {
		t.Fatal("continuation lost the saved session")
	}
	if continued.UsageInputTokens+continued.UsageOutputTokens <= used {
		t.Fatal("the raised budget did not let the assignment continue")
	}
}

// Missing usage is not free work. With a budget configured, an assignment whose
// consumption cannot be established waits for an owner decision, and raising
// the budget does not measure what the harness never reported.
func TestUnknownUsageHoldsUntilTheOwnerDecides(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	budget := int64(100000)
	b.cfg.TokenBudget = func() int64 { return budget }
	id := runningRun(t, b)
	// A turn the harness could not account for.
	if err := b.settleUsage(id, reserve(t, b, id), engine.Usage{}); err != nil {
		t.Fatal(err)
	}
	r, _ := b.snapshot(id)
	if r.UsageUnknownCalls != 1 || r.UsageInputTokens != 0 || r.UsageOutputTokens != 0 {
		t.Fatalf("an unmeasured turn was treated as free or invented: %+v", r.Run.Usage)
	}
	request := ""
	if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); !errors.Is(err, worker.ErrResourceHold) {
		t.Fatalf("unknown consumption under a budget did not hold: %v", err)
	}
	held, _ := b.snapshot(id)
	if held.Run.ResourceHold.Kind != worker.HoldUsageUnknown || !held.Run.ResourceHold.OwnerAction {
		t.Fatalf("an unmeasurable assignment did not become an owner decision: %+v", held.Run.ResourceHold)
	}
	budget = 10_000_000
	if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); !errors.Is(err, worker.ErrResourceHold) {
		t.Fatal("raising the budget erased the uncertainty")
	}
	budget = 0
	disabled, _ := b.snapshot(id)
	if hold := budgetHold(disabled, 0); hold != nil {
		t.Fatal("disabling the budget did not release the hold")
	}
	// Disabling it is the only way past: the assignment continues, and the
	// uncertainty it continues with stays on the record.
	continued := reserve(t, b, id)
	if err := b.settleUsage(id, continued, engineUsage(4, 2)); err != nil {
		t.Fatal(err)
	}
	final, _ := b.snapshot(id)
	if final.UsageUnknownCalls != 1 {
		t.Fatalf("continuing erased the recorded uncertainty: %d", final.UsageUnknownCalls)
	}
}

// Nothing is spent, and nothing new becomes unknown, when a session cannot be
// opened at all.
func TestFailedLaunchAddsNoUnknownConsumption(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	b.cfg.Engine = "codex"
	b.cfg.CodexBin = "/nonexistent/synthetic-codex"
	b.cfg.TokenBudget = func() int64 { return 100000 }
	id := runningRun(t, b)
	before, _ := b.snapshot(id)
	if _, err := b.sessionOptions(id, before, t.TempDir()); err == nil {
		t.Fatal("a missing CLI produced usable session options")
	}
	after, _ := b.snapshot(id)
	if after.UsageUnknownCalls != before.UsageUnknownCalls || after.ModelCalls != before.ModelCalls || after.PendingUsage != nil {
		t.Fatalf("a failed launch was accounted as consumption: unknown=%d calls=%d pending=%+v", after.UsageUnknownCalls, after.ModelCalls, after.PendingUsage)
	}
}

// A reservation that outlives its process is consumption that may have
// happened. Restart must convert it into uncertainty, and history recorded
// before the ledger existed must be converted exactly once.
func TestRestartConvertsStaleReservationsAndPreLedgerHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		run     storedRun
		unknown int
	}{
		{"stale reservation", storedRun{UsageLedger: true, ModelCalls: 3, UsageInputTokens: 30, PendingUsage: &pendingUsage{RequestID: "abandoned", Stage: stageNativeTurn}}, 1},
		{"pre-ledger history", storedRun{ModelCalls: 16}, 16},
		{"pre-ledger with reservation", storedRun{ModelCalls: 2, PendingUsage: &pendingUsage{RequestID: "abandoned"}}, 3},
		{"already reconciled", storedRun{UsageLedger: true, ModelCalls: 5, UsageInputTokens: 50}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := tc.run
			reconcileUsage(&run, 1000)
			if run.UsageUnknownCalls != tc.unknown || run.PendingUsage != nil || !run.UsageLedger {
				t.Fatalf("unknown=%d pending=%+v ledger=%v", run.UsageUnknownCalls, run.PendingUsage, run.UsageLedger)
			}
			if run.Run.Usage.TokenBudget != 1000 || run.Run.Usage.UnknownCalls != tc.unknown {
				t.Fatalf("published ledger disagrees: %+v", run.Run.Usage)
			}
			// Reconciling twice must not multiply the uncertainty.
			reconcileUsage(&run, 1000)
			if run.UsageUnknownCalls != tc.unknown {
				t.Fatal("second reconciliation double-counted", run.UsageUnknownCalls)
			}
		})
	}
}

// A settlement belongs to exactly one reservation. Anything else is a late or
// repeated report and must change nothing.
func TestUsageSettlesOncePerReservation(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	request := reserve(t, b, id)
	b.settleUsage(id, request, engineUsage(11, 7))
	b.settleUsage(id, request, engineUsage(11, 7))
	b.settleUsage(id, "a-different-request", engineUsage(500, 500))
	r, _ := b.snapshot(id)
	if r.UsageInputTokens != 11 || r.UsageOutputTokens != 7 || r.UsageUnknownCalls != 0 || r.PendingUsage != nil {
		t.Fatalf("ledger after repeated settlement: %+v", r.Run.Usage)
	}
}

// Owner pause and stop outrank a resource hold: an owner decision already made
// must not be overwritten by a wait discovered afterwards.
func TestOwnerControlsOutrankResourceHolds(t *testing.T) {
	for _, control := range []string{"paused", "cancelled"} {
		t.Run(control, func(t *testing.T) {
			b, _ := newFixture(t, "https://model.test", &fakeDocker{})
			defer b.Close()
			id := runningRun(t, b)
			if err := b.update(id, func(r *storedRun) error { r.PendingStatus = control; return nil }); err != nil {
				t.Fatal(err)
			}
			b.holdOnResources(id, worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, Reason: "Account allowance consumed"})
			r, _ := b.snapshot(id)
			if r.PendingStatus != control || r.Run.ResourceHold != nil {
				t.Fatalf("resource hold overwrote an owner control: %q", r.PendingStatus)
			}
			// It also refuses to authorize another turn, rather than racing the
			// control it just declined to overwrite.
			request := ""
			if err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &request); err == nil {
				t.Fatal("a controlled run authorized another turn")
			}
		})
	}
}

// A worker running a long build-and-test loop must not be stopped because it has
// run a certain number of commands. Per-command time and output bounds are what
// contain a command; a lifetime count is not a resource limit.
func TestCommandsAreNotCappedByCount(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	for i := 0; i < 128; i++ {
		if err := b.update(id, func(run *storedRun) error {
			run.Commands = append(run.Commands, commandRecord{Command: "synthetic", Success: true})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	result := invoke(t, b, id, "agent-one", "run_command", map[string]string{"command": "go test ./..."})
	if result.IsError {
		t.Fatalf("command 129 was refused: %s", result.Content)
	}
	after, _ := b.snapshot(id)
	if len(after.Commands) != 129 {
		t.Fatalf("command 129 was not recorded: %d", len(after.Commands))
	}
	if after.Commands[128].Command != "go test ./..." {
		t.Fatalf("command 129 did not run: %+v", after.Commands[128])
	}
}

// A settlement that did not persist leaves consumption unrecorded. Continuing to
// spend against a ledger the broker cannot vouch for is the thing to prevent, so
// the failure reaches the caller and the next turn is refused.
func TestUnpersistedSettlementHoldsTheNextTurn(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	request := reserve(t, b, id)
	if err := os.Chmod(b.cfg.StateDir, 0500); err != nil {
		t.Fatal(err)
	}
	settleErr := b.settleUsage(id, request, engineUsage(10, 5))
	if err := os.Chmod(b.cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if settleErr == nil {
		t.Fatal("a settlement that could not be written reported success")
	}

	// The reservation is retained rather than discarded, and the next turn is
	// refused while it is open, budget or not.
	held, _ := b.snapshot(id)
	if held.PendingUsage == nil {
		t.Fatal("the unsettled reservation was discarded rather than retained")
	}
	next := ""
	err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &next)
	if !errors.Is(err, worker.ErrResourceHold) || next != "" {
		t.Fatalf("another turn was authorized over an open reservation: %v", err)
	}
	after, _ := b.snapshot(id)
	if after.Run.ResourceHold == nil || after.Run.ResourceHold.Kind != worker.HoldUsageUnknown || !after.Run.ResourceHold.OwnerAction {
		t.Fatalf("an unresolved reservation was not surfaced for the owner: %+v", after.Run.ResourceHold)
	}
}

// A figure that is negative, incomplete or too large to add is not a
// measurement. Recording it as a number would make it indistinguishable from a
// real one later.
func TestUnusableUsageIsRecordedAsUnknownNotRepaired(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage engine.Usage
		seed  int64
	}{
		{"unknown", engine.Usage{InputTokens: 5, OutputTokens: 5}, 0},
		{"negative input", engine.Usage{InputTokens: -5, OutputTokens: 5, Known: true}, 0},
		{"negative output", engine.Usage{InputTokens: 5, OutputTokens: -5, Known: true}, 0},
		{"overflowing one column", engine.Usage{InputTokens: math.MaxInt64 / 2, OutputTokens: 1, Known: true}, math.MaxInt64 - 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newFixture(t, "https://model.test", &fakeDocker{})
			defer b.Close()
			id := runningRun(t, b)
			if err := b.update(id, func(r *storedRun) error { r.UsageInputTokens = tc.seed; return nil }); err != nil {
				t.Fatal(err)
			}
			if err := b.settleUsage(id, reserve(t, b, id), tc.usage); err != nil {
				t.Fatal(err)
			}
			r, _ := b.snapshot(id)
			if r.UsageUnknownCalls != 1 {
				t.Fatalf("unusable usage was accepted: unknown=%d", r.UsageUnknownCalls)
			}
			if r.UsageInputTokens != tc.seed || r.UsageOutputTokens != 0 {
				t.Fatalf("unusable usage changed the total: %d/%d", r.UsageInputTokens, r.UsageOutputTokens)
			}
			if r.PendingUsage != nil {
				t.Fatal("the reservation was left open after an unknown settlement")
			}
		})
	}
}

// Two columns that are each representable can still overflow the total the
// budget and the owner's view are computed from.
func TestCombinedTotalOverflowIsUnknownNotWrapped(t *testing.T) {
	b, _ := newFixture(t, "https://model.test", &fakeDocker{})
	defer b.Close()
	id := runningRun(t, b)
	half := int64(math.MaxInt64/2 + 1)
	if err := b.update(id, func(r *storedRun) error {
		r.UsageInputTokens, r.UsageOutputTokens = half, half-1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.settleUsage(id, reserve(t, b, id), engineUsage(1, 1)); err != nil {
		t.Fatal(err)
	}
	r, _ := b.snapshot(id)
	if r.UsageInputTokens+r.UsageOutputTokens < 0 {
		t.Fatalf("the combined total wrapped negative: %d + %d", r.UsageInputTokens, r.UsageOutputTokens)
	}
	if r.UsageUnknownCalls != 1 {
		t.Fatalf("a total that cannot be represented was accepted: unknown=%d", r.UsageUnknownCalls)
	}
	if r.UsageInputTokens != half || r.UsageOutputTokens != half-1 {
		t.Fatalf("the ledger changed anyway: %d/%d", r.UsageInputTokens, r.UsageOutputTokens)
	}
}

// A reservation whose settlement never persisted must not make every resume hold
// against the same unresolved record. The explicit resume closes it once, as
// unknown consumption — which a configured budget then rightly stops on, and a
// disabled one rightly continues past.
func TestResumeAfterRepairedStorageResolvesTheStaleReservation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		budget   int64
		proceeds bool
	}{
		{"budget enabled holds on the unknown", 100000, false},
		{"budget disabled continues", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newFixture(t, "https://model.test", &fakeDocker{})
			defer b.Close()
			b.cfg.TokenBudget = func() int64 { return tc.budget }
			id := runningRun(t, b)
			// The state a transient write failure leaves behind: a reservation
			// recorded, its settlement lost, and the run stopped for the owner.
			if err := b.update(id, func(r *storedRun) error {
				r.Run.Status = "usage_wait"
				r.Run.Summary = "A previous model request's consumption was never recorded"
				r.PendingUsage = &pendingUsage{RequestID: "lost-settlement", Stage: stageNativeTurn, StartedAt: now()}
				r.Run.ResourceHold = &worker.ResourceHold{Kind: worker.HoldUsageUnknown, OwnerAction: true, Reason: "unresolved accounting"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if code := request(t, b, "/runs/"+id+"/resume", "owner-resume", map[string]string{"instruction": "Continue"}).Code; code != 200 {
				t.Fatal("the supported recovery path refused an explicit resume", code)
			}
			settled, _ := b.snapshot(id)
			if settled.PendingUsage != nil {
				t.Fatal("the stale reservation survived an explicit resume")
			}
			if settled.UsageUnknownCalls != 1 {
				t.Fatalf("a possibly-billed request was written off as free: unknown=%d", settled.UsageUnknownCalls)
			}
			if err := b.update(id, func(r *storedRun) error { r.Run.Status = "running"; return nil }); err != nil {
				t.Fatal(err)
			}
			next := ""
			err := b.reserveWorkerModelCall(context.Background(), id, stageNativeTurn, &next)
			if tc.proceeds {
				if err != nil {
					t.Fatalf("a disabled budget still held on recorded uncertainty: %v", err)
				}
				working, _ := b.snapshot(id)
				if working.UsageUnknownCalls != 1 {
					t.Fatal("continuing erased the recorded uncertainty", working.UsageUnknownCalls)
				}
				return
			}
			if !errors.Is(err, worker.ErrResourceHold) {
				t.Fatalf("unknown consumption under a budget did not hold: %v", err)
			}
			held, _ := b.snapshot(id)
			if held.Run.ResourceHold == nil || held.Run.ResourceHold.Kind != worker.HoldUsageUnknown {
				t.Fatalf("unknown consumption under a budget did not hold: %+v", held.Run.ResourceHold)
			}
		})
	}
}
