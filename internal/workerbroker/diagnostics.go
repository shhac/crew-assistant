package workerbroker

import "github.com/shhac/crew-assistant/internal/diagnostics"

func (b *Broker) reportFailure(id, stage string, err error) {
	event := diagnostics.Event{Component: "worker", Stage: stage, ProjectID: b.cfg.ProjectID, RunID: id, Engine: b.cfg.Engine}
	if run, getErr := b.snapshot(id); getErr == nil {
		event.ModelCalls = run.ModelCalls
		status := run.Run.Status
		if run.PendingStatus != "" {
			status = run.PendingStatus
		}
		// Resumes retain the previous retry timestamp until a successful model
		// call. A timestamp alone therefore does not establish another recovery.
		if status == "retry_wait" && run.Run.RetryAt.After(now()) {
			retryAt := run.Run.RetryAt
			event.RetryAt = &retryAt
		}
	}
	b.cfg.Diagnostics.Failure(event, err)
}
