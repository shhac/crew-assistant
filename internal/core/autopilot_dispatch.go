package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AutopilotDispatch fences dispatch across independently opened stores.
type AutopilotDispatch struct {
	Owner     string             `json:"owner"`
	Token     uint64             `json:"token"`
	Purpose   string             `json:"purpose"`
	ActorKind AutopilotActorKind `json:"actor_kind"`
	ExpiresAt time.Time          `json:"expires_at"`
}

type ExternalCallback struct {
	ID, Kind, ProjectID string
	StartedAt           time.Time
}

func externalCallbackKey(a AutopilotAction) string {
	return fmt.Sprintf("%s:%s:%d", a.ID, a.Dispatch.Owner, a.Dispatch.Token)
}

// admitExternal is called with store admission held, before intent commits.
func (s *Service) admitExternal(a AutopilotAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.externalClosed || s.upgradeDraining {
		return autopilotDeferred{ErrConflict}
	}
	if s.external == nil {
		s.external = map[string]ExternalCallback{}
	}
	key := externalCallbackKey(a)
	if _, exists := s.external[key]; exists {
		return ErrConflict
	}
	s.external[key] = ExternalCallback{a.ID, a.Action.Kind, a.Action.ProjectID, s.now().UTC()}
	if s.externalChanged == nil {
		s.externalChanged = make(chan struct{})
	}
	return nil
}

func (s *Service) releaseExternal(a AutopilotAction) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.external, externalCallbackKey(a))
	if s.externalChanged != nil {
		close(s.externalChanged)
	}
	s.externalChanged = make(chan struct{})
}

func (s *Service) ExternalCallbacks() []ExternalCallback {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExternalCallback, 0, len(s.external))
	for _, a := range s.external {
		out = append(out, a)
	}
	return out
}

// OpenExternalAdmission starts a new runtime after the previous drain completed.
func (s *Service) OpenExternalAdmission() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.external) == 0 && !s.upgradeDraining {
		s.externalClosed = false
	}
}

func (s *Service) CloseExternalAdmission() {
	s.mu.Lock()
	s.externalClosed = true
	s.mu.Unlock()
}

// DrainExternal waits through receipt persistence, including HTTP approvals.
func (s *Service) DrainExternal() {
	s.CloseExternalAdmission()
	for {
		s.mu.RLock()
		n, changed := len(s.external), s.externalChanged
		s.mu.RUnlock()
		if n == 0 {
			return
		}
		<-changed
	}
}

func (c *AutopilotCoordinator) SetCallbackContext(ctx context.Context) {
	c.mu.Lock()
	c.callbackContext = ctx
	c.mu.Unlock()
}

func (c *AutopilotCoordinator) grantDispatch(a *AutopilotAction, purpose string, kind AutopilotActorKind) {
	a.DispatchToken++
	a.Dispatch = &AutopilotDispatch{c.instance, a.DispatchToken, purpose, kind, c.service.now().UTC().Add(c.leaseFor)}
}

func (c *AutopilotCoordinator) ownsDispatch(a, current AutopilotAction) bool {
	return a.Dispatch != nil && current.Dispatch != nil && current.Dispatch.Owner == c.instance && current.Dispatch.Token == a.Dispatch.Token
}

// Renewal changes only the lease, without flooding the audit history.
func (c *AutopilotCoordinator) renewDispatch(a AutopilotAction) func() {
	done, exited := make(chan struct{}), make(chan struct{})
	interval := c.renewEvery
	if interval <= 0 {
		return func() {}
	}
	go func() {
		defer close(exited)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			owned := false
			err := c.service.store.updateAutopilotRecords(context.Background(), false, func(conn *sql.Conn) error {
				current, err := readAutopilotAction(context.Background(), conn, "id", a.ID)
				if err != nil {
					return err
				}
				if !c.ownsDispatch(a, current) || !current.Dispatch.ExpiresAt.After(c.service.now()) {
					return nil
				}
				owned = true
				current.Dispatch.ExpiresAt = c.service.now().UTC().Add(c.leaseFor)
				data, err := json.Marshal(current)
				if err != nil {
					return err
				}
				_, err = conn.ExecContext(context.Background(), "UPDATE autopilot_actions SET payload=? WHERE id=?", string(data), a.ID)
				return err
			})
			if err == nil && !owned {
				return
			}
		}
	}()
	return func() { close(done); <-exited }
}
