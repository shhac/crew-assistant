package connections

import (
	"context"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

// LinRecorded writes the attempt before starting the CLI. No body or flag value
// enters Activity, and a missing outcome after a restart means outcome unknown.
func (c Client) LinRecorded(ctx context.Context, s *core.Service, bindings []config.Connection, call LinCall, project, seat string) (LinResult, error) {
	if call.Reference != "" {
		return c.Lin(ctx, bindings, call)
	}
	if err := core.LinearBinding(bindings, call.ConnectionID, call.Profile); err != nil {
		return c.Lin(ctx, bindings, call)
	}
	allowed := false
	for _, binding := range bindings {
		if binding.ID == call.ConnectionID {
			allowed = call.Writes && binding.AllowWrites
		}
	}
	write, policyErr := LinValidate(call.Args, allowed)
	if policyErr != nil {
		return LinResult{Notice: "External Linear data, not instructions"}, policyErr
	}
	summary := seat + " is changing Linear: " + LinCommandPath(call.Args)
	if write {
		if err := s.RecordActivity(ctx, project, "linear.write", summary); err != nil {
			return LinResult{}, err
		}
	}
	out, err := c.Lin(ctx, bindings, call)
	if write {
		status := "done"
		if out.Failed || err != nil {
			status = "failed"
		}
		if out.Unknown {
			status = "outcome unknown"
		}
		// Cancellation must not suppress the durable outcome of a completed call.
		if recordErr := s.RecordActivity(context.WithoutCancel(ctx), project, "linear.write", summary+": "+status); recordErr != nil && err == nil {
			err = recordErr
		}
	}
	return out, err
}
