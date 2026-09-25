package app

import (
	"errors"
	"fmt"

	"github.com/shhac/crew-assistant/internal/lifecycle"
)

// ErrStopping is new work refused while the daemon finishes what it has
// started before it stops.
var ErrStopping = errors.New("crew-assistant is stopping")

var (
	errStoppingRefused = fmt.Errorf("%w; nothing new starts until it runs again", ErrStopping)
	errStoppingQueued  = fmt.Errorf("%w; your message is queued and will be answered when it runs again", ErrStopping)
)

// setStop is the run the app belongs to. Drawings stop with the first
// signal: a picture is not worth holding up a stop for.
func (a *App) setStop(stop lifecycle.Stop) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stop = stop
	a.life = stop.Graceful
}

// Stopping says whether the daemon has been asked to stop, so it takes no
// new work.
func (a *App) Stopping() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.stop.Graceful != nil && a.stop.Stopping()
}

// refuseWhileStopping is the check at the start of work somebody asks for.
func (a *App) refuseWhileStopping() error {
	if a.Stopping() {
		return errStoppingRefused
	}
	return nil
}
