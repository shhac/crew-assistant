// Package lifecycle is the daemon's two-stage stop. The first signal stops
// new work being taken and lets the work in progress finish; the second ends
// that work too. Two contexts carry the two stages, and confusing them either
// kills a turn the first Ctrl-C promised to wait for or starts one it
// promised not to.
package lifecycle

import (
	"context"
	"os"
	"syscall"
	"time"
)

// Stop is the pair every long-running part of the daemon takes. Work is
// taken only while Graceful lasts; work already taken runs on Force.
type Stop struct {
	Graceful, Force context.Context
}

// Stopping says whether new work should be refused. It is asked explicitly
// before taking work rather than left to a select, because a select with a
// ready case picks one at random and would still start new work about half
// the time after a stop was asked for.
func (s Stop) Stopping() bool { return s.Graceful.Err() != nil }

// Now is a Stop whose two stages are one: what a caller with only one
// context, such as a test or a one-shot command, hands over.
func Now(ctx context.Context) Stop { return Stop{Graceful: ctx, Force: ctx} }

// Signals are what the daemon stops on. SIGTERM matters as much as SIGINT:
// it is what launchd, systemd and a reboot send, and it should let the work
// in progress finish too.
var Signals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// ForceDrain bounds how long a forced stop waits for cancelled work to end,
// so a wedged step can't hold up the exit it was asked to force.
var ForceDrain = 5 * time.Second

// Watch turns signals into a Stop: the first ends Graceful, the second
// Force; ctx ending ends both. say tells the owner what each one did.
// cancelAll ends both, for a daemon stopping for its own reasons.
func Watch(ctx context.Context, signals <-chan os.Signal, say func(string)) (stop Stop, cancelAll func()) {
	graceful, endGraceful := context.WithCancel(ctx)
	force, endForce := context.WithCancel(ctx)
	go func() {
		select {
		case <-ctx.Done():
		case <-force.Done():
			return
		case <-signals:
			say("Stopping: finishing the work in progress; nothing new starts. Press Ctrl-C again to stop now.")
			endGraceful()
			select {
			case <-ctx.Done():
			case <-force.Done():
				return
			case <-signals:
				say("Stopping now: cancelling the work in progress.")
			}
		}
		endGraceful()
		endForce()
	}()
	return Stop{Graceful: graceful, Force: force}, func() {
		endGraceful()
		endForce()
	}
}
