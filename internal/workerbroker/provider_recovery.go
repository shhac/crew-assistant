package workerbroker

import (
	"time"

	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/lib-agent-harness/session"
)

// What happens when a worker's coding session fails.
//
// Nothing is retried automatically. The previous contract drove workers through
// a request-per-proposal transport, where repeating a refused HTTP request was a
// safe no-op and automatic recovery was worth having. A native turn is not that:
// by the time it fails, the worker may have edited files and run commands, and
// starting it again would do all of that a second time against a workspace that
// already carries the first attempt. So every failure stops the assignment with
// its work preserved, and a person decides.
//
// What this file does care about is that the harness's own classification
// survives to the owner. A coding CLI reports expired logins, lost processes and
// refused capability checks in a fixed vocabulary; reducing all of that to
// "something went wrong" is how an operator ends up with a diagnostic they
// cannot act on.

// classifiedFailure is what a failed attempt established.
type classifiedFailure struct {
	// facts are the harness's own, when it supplied any.
	facts session.Facts
	known bool
	// evidence names what was available to classify, never a reconstructed cause.
	evidence string
	// message is safe to persist and display: it comes from fixed vocabulary.
	message string
}

func classifyModelFailure(err error) classifiedFailure {
	facts, known := session.ErrorFacts(err)
	if !known {
		// Something outside this library's vocabulary. Saying so is better than
		// picking a category, because a category implies a remedy.
		return classifiedFailure{
			evidence: evidenceUntyped,
			message:  "the worker's coding session failed for a reason the daemon could not classify",
		}
	}
	return classifiedFailure{
		facts: facts, known: true,
		evidence: nativeEvidence(facts.Kind),
		// Every one of these errors is built from constants; no harness output,
		// provider prose or path reaches one.
		message: err.Error(),
	}
}

func nativeEvidence(kind string) string {
	switch kind {
	case session.FailureCapability:
		return evidenceCapabilityCheck
	case session.FailureProcess:
		return evidenceLocalProcess
	default:
		return evidenceNativeHarness
	}
}

func (b *Broker) modelFailureAt(id, stage string, err error) {
	b.reportFailure(id, stage, err)
	b.blockOnModelFailure(id, classifyModelFailure(err))
}

// blockOnModelFailure stops the run and preserves its work.
func (b *Broker) blockOnModelFailure(id string, cls classifiedFailure) {
	_ = b.update(id, func(r *storedRun) error {
		if r.Run.Status == "cancelled" || r.PendingStatus == "paused" || r.PendingStatus == "cancelled" {
			return nil
		}
		setFailureDetails(&r.Run, b.cfg.Engine, cls)
		r.Run.ModelFailureEvidence = cls.evidence
		r.Run.RetryAt = time.Time{}
		r.PendingStatus = "blocked"
		r.PendingSummary = cls.message + ". Inspect the preserved work and correct the problem before explicitly resuming; no automatic retry is scheduled."
		r.Run.Summary = "Finalizing isolated execution and collecting evidence"
		r.Run.UpdatedAt = now()
		return nil
	})
}

func (b *Broker) clearProviderFailure(id string) {
	_ = b.update(id, func(r *storedRun) error {
		r.Run.ProviderFailures = 0
		r.Run.ProviderFailureKind = ""
		r.Run.ModelFailureEngine, r.Run.ModelFailurePhase, r.Run.ModelFailureCode = "", "", ""
		r.Run.ModelFailureEvidence = ""
		r.Run.ModelExitCode = nil
		r.Run.RetryAt = time.Time{}
		return nil
	})
}

// setFailureDetails records the harness's own facts. These are the codes that
// reach the owner's inspect output and dashboard, so losing them here is losing
// them everywhere.
func setFailureDetails(run *worker.Run, configuredEngine string, cls classifiedFailure) {
	run.ModelFailureEngine = configuredEngine
	run.ModelFailurePhase, run.ModelFailureCode = "", ""
	run.ModelExitCode = nil
	if !cls.known {
		run.ProviderFailureKind = ""
		return
	}
	run.ProviderFailureKind = cls.facts.Kind
	if cls.facts.Engine != "" {
		run.ModelFailureEngine = cls.facts.Engine
	}
	run.ModelFailureCode = cls.facts.Code
	// Not every failure has a phase; the family is what it happened during, and
	// an empty field beside a code an owner is trying to place is unhelpful.
	run.ModelFailurePhase = cls.facts.Phase
	if run.ModelFailurePhase == "" {
		run.ModelFailurePhase = cls.facts.Kind
	}
	if cls.facts.ExitCode != nil {
		code := *cls.facts.ExitCode
		run.ModelExitCode = &code
	}
}

// Failure evidence names what was available to classify with, never a
// reconstructed cause. The distinction an owner needs is between a failure the
// harness described and one that arrived as an opaque error, because only the
// first tells them what to change.
const (
	// evidenceNativeHarness: the coding harness classified it, from its own
	// fixed vocabulary.
	evidenceNativeHarness = "native_harness"
	// evidenceLocalProcess: the harness process itself ended.
	evidenceLocalProcess = "local_process"
	// evidenceUntyped: nothing classified it. This is not a cause; it is the
	// absence of one, and it is worth showing as such.
	evidenceUntyped = "untyped_error"
)
