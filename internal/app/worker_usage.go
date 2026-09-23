package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/quota"
)

var errWorkerUsageHeld = errors.New("worker work held by subscription usage policy")

// workerUsageAllowed runs BEFORE durable dispatch/resume/instruction intent.
// A hold leaves the existing queued/interrupted state eligible for the next tick.
func (a *App) workerUsageAllowed(ctx context.Context, profileID string) error {
	profile, err := a.Core.GetProfile(profileID)
	if err != nil {
		return err
	}
	hold, err := a.workerHeadroom(ctx, headroomSubject{StatusID: profile.ID, Name: profile.Name, Model: workerModel(a.Config(), profile), Inspectable: profile.Managed}, true)
	if err != nil {
		return err
	}
	if hold != nil {
		return fmt.Errorf("%w: %s", errWorkerUsageHeld, hold.Reason)
	}
	return nil
}

// workerInferenceAdmission is injected into every managed broker and runs
// before each of its model requests, including context summaries and recovery
// attempts. It applies exactly the policy that gates new worker work, so a
// worker cannot keep consuming an account that is already too full to start on.
//
// The model comes from the broker, not from current configuration. A broker
// keeps the engine, binary and login it was created with until it is restarted,
// so reading a since-edited profile would measure headroom on an account this
// worker is not spending from. Only the thresholds are read live.
func (a *App) workerInferenceAdmission(ctx context.Context, projectID string, model config.Model) error {
	hold, err := a.workerHeadroom(ctx, headroomSubject{StatusID: "managed-" + projectID, Name: "this project's worker", Model: model, Inspectable: true}, false)
	if err != nil {
		return err
	}
	if hold != nil {
		return &worker.HoldError{Hold: *hold}
	}
	return nil
}

// headroomSubject names the account a decision is about. Inspectable is false
// for a broker whose CLI login this host cannot read, such as an external one.
type headroomSubject struct {
	StatusID, Name string
	Model          config.Model
	Inspectable    bool
}

// workerHeadroom is the one subscription decision, shared by the admission that
// precedes new worker work and by the admission the broker runs before every
// inference. report controls only whether the dashboard integration row is
// rewritten: a per-inference check must not rewrite that row on every call.
func (a *App) workerHeadroom(ctx context.Context, subject headroomSubject, report bool) (*worker.ResourceHold, error) {
	policy := a.Config().Limits.WorkerUsage
	id, name := "worker-usage:"+subject.StatusID, "Usage for "+subject.Name
	status := func(state, detail string) {
		if report {
			a.Status(id, name, state, detail)
		}
	}
	unavailable := func(reason string) (*worker.ResourceHold, error) {
		detail := reason + "; new worker work is allowed"
		if policy.OnUnavailable == "pause" {
			detail = reason + "; new worker work is paused"
		}
		status("unavailable", detail)
		if policy.OnUnavailable != "pause" {
			return nil, nil
		}
		// A telemetry outage is a measurement problem, not a decision. It has no
		// known reset, but it can end on its own, so the daemon keeps looking and
		// recovers by itself when readings return or the owner relaxes the policy.
		return &worker.ResourceHold{Kind: worker.HoldTelemetryUnavailable, Reason: detail, NextCheckAt: time.Now().Add(quota.CacheAge).UTC()}, nil
	}
	if !subject.Inspectable {
		return unavailable("The external broker's CLI account cannot be inspected locally")
	}
	model := subject.Model
	threshold, supported := quota.Threshold(policy, model.Engine)
	if !supported {
		return unavailable("Subscription usage is unavailable for this engine")
	}
	if threshold == 0 {
		status("disabled", "Worker usage limit is disabled for "+model.Engine)
		return nil, nil
	}
	snapshot := a.workerUsage.Read(ctx, model)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	verdict := quota.Evaluate(snapshot, model, threshold, time.Now())
	if verdict.Held {
		detail := "New worker work paused: " + verdict.Detail
		status("paused", detail)
		return &worker.ResourceHold{Kind: worker.HoldSubscriptionQuota, Reason: detail, ResetsAt: verdict.ResetsAt, NextCheckAt: time.Now().Add(quota.CacheAge).UTC()}, nil
	}
	if !verdict.Known {
		return unavailable("Fresh subscription usage is unavailable for " + model.Engine)
	}
	status("connected", fmt.Sprintf("%s; new worker work pauses at %d%% consumed", verdict.Detail, threshold))
	return nil, nil
}
