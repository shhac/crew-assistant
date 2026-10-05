package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/upgradestate"
)

const DecisionUpgradeAvailable = "upgrade-available"
const ChoiceUpgradeByHand = "I'll upgrade by hand"
const DecisionUpgradeFailed = "upgrade-failed"

func UpgradeChoice(version string) string      { return "Upgrade to " + version }
func RetryUpgradeChoice(version string) string { return "Try " + version + " again" }

// OnUpgradeRequested is installed only when this daemon can self-upgradestate.
func (s *Service) OnUpgradeRequested(fn func(string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upgradeRequested = fn
}

func (s *Service) upgradeHook() func(string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upgradeRequested
}

func trimVersion(v string) string { return strings.TrimPrefix(v, "v") }

// AdmitUpgrade serializes the final eligibility/quiet check and drain with
// owner decisions, release checks and every new work claim. The callback must
// not read or mutate core state: it receives the current snapshot instead.
// Work already claimed remains free to finish after admission closes.
func (s *Service) AdmitUpgrade(ctx context.Context, request func(Snapshot, config.Config) (bool, error)) (bool, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.upgradeDraining {
		return false, upgradestate.ErrInProgress
	}
	v, err := s.store.Snapshot(ctx)
	if err != nil {
		return false, err
	}
	for _, callback := range s.external {
		v.ExternalCallbacks = append(v.ExternalCallbacks, callback)
	}
	admitted, err := request(v, s.cfg)
	if admitted && err == nil {
		s.upgradeDraining = true
	}
	return admitted, err
}

// Claimed work counts as busy even before its model observer starts. This
// closes the gap between durable admission and a live turn appearing.
func UpgradeWorkClaimed(v Snapshot) bool {
	if len(v.ExternalCallbacks) > 0 {
		return true
	}
	for _, t := range v.Tasks {
		if len(t.Claims) > 0 {
			return true
		}
	}
	for _, p := range v.Projects {
		if len(p.Claims) > 0 {
			return true
		}
	}
	return false
}

type UpgradeProgress struct {
	Step      upgradestate.Step        `json:"step"`
	From      string                   `json:"from"`
	To        string                   `json:"to"`
	Since     time.Time                `json:"since"`
	WaitingOn []upgradestate.WaitingOn `json:"waiting_on"`
}
type RollbackStatus struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Failure string `json:"failure"`
	Clear   string `json:"clear"`
}

// UpdateStatus persists checks and owner choices. Running, Mode and Unavailable
// are supplied by the app for the current boot only.
type UpgradeRequest struct {
	DecisionID string    `json:"decision_id"`
	Version    string    `json:"version"`
	Choice     string    `json:"choice"`
	At         time.Time `json:"at"`
}

type UpdateStatus struct {
	PendingRequest  *UpgradeRequest `json:"pending_request,omitempty"`
	Failed          string          `json:"failed,omitempty"`
	Available       string          `json:"available,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	URL             string          `json:"url,omitempty"`
	CheckedAt       time.Time       `json:"checked_at,omitzero"`
	Error           string          `json:"error,omitempty"`
	Skipped         string          `json:"skipped,omitempty"`
	DecisionVersion string          `json:"decision_version,omitempty"`
	Running         string          `json:"running,omitempty"`
	Mode            string          `json:"mode,omitempty"`
	Unavailable     string          `json:"unavailable,omitempty"`
}

// ReconcileRunningUpdate closes a notice after a manual upgrade, even offline.
func (s *Service) ReconcileRunningUpdate(ctx context.Context, running string) error {
	if !ValidVersion(running) {
		return nil
	}
	return s.store.update(ctx, func(v *Snapshot) error { reconcileRunningUpdate(v, running, s.now().UTC()); return nil })
}
func reconcileRunningUpdate(v *Snapshot, running string, now time.Time) {
	for i := range v.Decisions {
		d := &v.Decisions[i]
		if d.Kind == DecisionUpgradeFailed && d.Status == DecisionOpen && len(d.Choices) > 1 {
			target := strings.TrimSuffix(strings.TrimPrefix(d.Choices[1], "Try "), " again")
			if ValidVersion(target) && CompareVersions(running, target) >= 0 {
				d.Status, d.Disposition, d.ResolvedAt = DecisionResolved, DispositionCompleted, &now
				d.ResolutionReason = "The running version includes this update."
				record(v, now, "", "decision.resolved", d.Title+": "+d.ResolutionReason)
			}
		}
	}
	if v.Update.Available == "" || CompareVersions(running, v.Update.Available) < 0 {
		return
	}
	closeUpgradeDecisions(v, now, DispositionCompleted, "The running version includes this update.")
	v.Update.Available, v.Update.Notes, v.Update.URL, v.Update.Skipped, v.Update.DecisionVersion = "", "", "", "", ""
}
func closeUpgradeDecisions(v *Snapshot, now time.Time, disposition, reason string) {
	for i := range v.Decisions {
		d := &v.Decisions[i]
		if d.Kind == DecisionUpgradeAvailable && d.Status == DecisionOpen {
			d.Status, d.Disposition, d.ResolvedAt = DecisionResolved, disposition, &now
			d.ResolutionReason = reason
			record(v, now, "", "decision.resolved", d.Title+": "+reason)
		}
	}
}

// RecordUpdateCheck saves the result and its decision in a single transaction.
// Failures preserve every prior result and decision except error and date.
func (s *Service) RecordUpdateCheck(ctx context.Context, running string, result upgradestate.Result) error {
	if !ValidVersion(running) {
		return nil
	}
	cfg := s.configuration().Upgrade
	return s.store.update(ctx, func(v *Snapshot) error {
		now := s.now().UTC()
		u := &v.Update
		u.CheckedAt, u.Error = result.CheckedAt, result.Error
		if result.Error != "" {
			return nil
		}
		reconcileRunningUpdate(v, running, now)
		if !ValidVersion(result.Available) || CompareVersions(result.Available, running) <= 0 {
			return nil
		}
		version := "v" + trimVersion(result.Available)
		// A temporarily older formula must not replace a newer known release.
		if u.Available != "" && CompareVersions(version, u.Available) < 0 {
			return nil
		}
		if u.Available != "" && CompareVersions(version, u.Available) > 0 {
			closeUpgradeDecisions(v, now, DispositionSuperseded, "Superseded by "+version+".")
		}
		u.Available, u.Notes, u.URL = version, result.Notes, result.URL
		// Notices survive restarts; refresh capability when a Homebrew install
		// replaces a standalone install, or the owner moves the binary out of it.
		for i := range v.Decisions {
			d := &v.Decisions[i]
			if d.Kind != DecisionUpgradeAvailable || d.Status != DecisionOpen || u.DecisionVersion != version {
				continue
			}
			d.Choices = []string{ChoiceUpgradeByHand, "Skip " + version}
			d.Recommendation = "Upgrade by hand when you're ready."
			if s.upgradeHook() != nil {
				d.Choices = append([]string{UpgradeChoice(version)}, d.Choices...)
				d.Recommendation = UpgradeChoice(version)
			}
		}
		if cfg.Mode != "ask" || u.Skipped == version || u.DecisionVersion == version {
			return nil
		}
		d := Decision{ID: uid(), Kind: DecisionUpgradeAvailable, Title: version + " is available", Context: fmt.Sprintf("Running v%s; %s is available.\n\n%s\n\nRelease: %s\n\nUpgrade by hand: brew upgrade %s\nThen restart crew-assistant. For a standalone install, download the release by hand.", trimVersion(running), version, result.Notes, result.URL, cfg.Formula), Recommendation: "Upgrade by hand when you're ready.", Choices: []string{ChoiceUpgradeByHand, "Skip " + version}, Status: DecisionOpen, CreatedAt: now}
		v.Decisions = append(v.Decisions, d)
		if s.upgradeHook() != nil {
			d := &v.Decisions[len(v.Decisions)-1]
			d.Choices = append([]string{UpgradeChoice(version)}, d.Choices...)
			d.Recommendation = UpgradeChoice(version)
			d.Context += "\n\nUpgrade here: finish running work, back up state and config, install with Homebrew, then check health and restore the previous version if needed."
		}
		u.DecisionVersion = version
		record(v, now, "", "decision.opened", d.Title)
		return nil
	})
}

// RecordUpgradeFailure uses an attempt key to survive a crash between this
// transaction and marking the external record. Notes never drive this decision.
func (s *Service) RecordUpgradeFailure(ctx context.Context, r upgradestate.Record) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		key := "upgrade-failed:" + r.StartedAt.UTC().Format(time.RFC3339Nano)
		if v.Events[key] {
			return nil
		}
		if !ValidVersion(v.Update.Failed) || CompareVersions(r.To, v.Update.Failed) > 0 {
			v.Update.Failed = r.To
		}
		now := s.now().UTC()
		context := fmt.Sprintf("Upgrade to %s failed: %s. Restored %s.", r.To, r.Failure, r.From)
		if r.Pinned {
			context += " Rollback is in force. Clear it with crew-assistant upgrade clear-rollback (or choose Try again)."
		}
		if r.DetachedLog != "" {
			context += " Detached daemon log: " + r.DetachedLog
		}
		v.Decisions = append(v.Decisions, Decision{ID: uid(), Kind: DecisionUpgradeFailed, Title: "Upgrade to " + r.To + " failed", Context: context, Recommendation: "Stay on " + r.From, Choices: []string{"Stay on " + r.From, RetryUpgradeChoice(r.To)}, Status: DecisionOpen, CreatedAt: now})
		if v.Events == nil {
			v.Events = map[string]bool{}
		}
		v.Events[key] = true
		record(v, now, "", "decision.opened", "Upgrade to "+r.To+" failed")
		return nil
	})
}

// ReofferAbandonedUpgrade allows the next check to offer an upgrade whose
// committed choice was interrupted by an owner signal before installation.
func (s *Service) ReofferAbandonedUpgrade(ctx context.Context, target string) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		if v.Update.DecisionVersion != target {
			return nil
		}
		for _, d := range v.Decisions {
			if d.Kind == DecisionUpgradeAvailable && d.Status == DecisionResolved && d.Answer == UpgradeChoice(target) {
				v.Update.DecisionVersion = ""
				break
			}
		}
		return nil
	})
}

// The decision and intent are committed together. If no matching attempt was
// started after that commit, startup reoffers the owner's original choice.
func (s *Service) ReconcileUpgradeRequests(ctx context.Context, running string, r *upgradestate.Record) error {
	return s.store.update(ctx, func(v *Snapshot) error {
		pending := v.Update.PendingRequest
		if pending == nil {
			return nil
		}
		started := r != nil && r.To == pending.Version && !r.StartedAt.Before(pending.At) && r.Step != upgradestate.Abandoned
		if !started {
			d := decision(v, pending.DecisionID)
			if d != nil && d.Status == DecisionResolved && d.Answer == pending.Choice {
				now := s.now().UTC()
				if ValidVersion(running) && CompareVersions(running, pending.Version) >= 0 {
					d.Disposition = DispositionCompleted
					d.ResolutionReason = "The running version includes this update."
				} else if d.Kind == DecisionUpgradeAvailable && v.Update.DecisionVersion != pending.Version {
					d.Disposition = DispositionSuperseded
					d.ResolutionReason = "Superseded by a newer update."
				} else {
					d.Status = DecisionOpen
					d.ResolvedAt = nil
					d.Disposition = ""
					d.Answer = ""
					d.AnsweredBy = ""
					d.ResolutionReason = ""
					record(v, now, "", "decision.opened", d.Title+": the requested upgrade had not started")
				}
			}
		}
		v.Update.PendingRequest = nil
		return nil
	})
}

func upgradeDecisionVersion(d Decision) string {
	for _, choice := range d.Choices {
		var version string
		switch {
		case strings.HasPrefix(choice, "Upgrade to "):
			version = strings.TrimPrefix(choice, "Upgrade to ")
		case strings.HasPrefix(choice, "Skip "):
			version = strings.TrimPrefix(choice, "Skip ")
		case strings.HasPrefix(choice, "Try ") && strings.HasSuffix(choice, " again"):
			version = strings.TrimSuffix(strings.TrimPrefix(choice, "Try "), " again")
		}
		if ValidVersion(version) {
			return version
		}
	}
	return ""
}
