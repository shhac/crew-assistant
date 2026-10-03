package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/upgrade"
)

const DecisionUpgradeAvailable = "upgrade-available"
const ChoiceUpgradeByHand = "I'll upgrade by hand"

func trimVersion(v string) string { return strings.TrimPrefix(v, "v") }

// UpdateStatus persists checks and owner choices. Running, Mode and Unavailable
// are supplied by the app for the current boot only.
type UpdateStatus struct {
	Available       string    `json:"available,omitempty"`
	Notes           string    `json:"notes,omitempty"`
	URL             string    `json:"url,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitzero"`
	Error           string    `json:"error,omitempty"`
	Skipped         string    `json:"skipped,omitempty"`
	DecisionVersion string    `json:"decision_version,omitempty"`
	Running         string    `json:"running,omitempty"`
	Mode            string    `json:"mode,omitempty"`
	Unavailable     string    `json:"unavailable,omitempty"`
}

// ReconcileRunningUpdate closes a notice after a manual upgrade, even offline.
func (s *Service) ReconcileRunningUpdate(ctx context.Context, running string) error {
	if !ValidVersion(running) {
		return nil
	}
	return s.store.update(ctx, func(v *Snapshot) error { reconcileRunningUpdate(v, running, s.now().UTC()); return nil })
}
func reconcileRunningUpdate(v *Snapshot, running string, now time.Time) {
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
func (s *Service) RecordUpdateCheck(ctx context.Context, running string, result upgrade.Result) error {
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
		if cfg.Mode != "ask" || u.Skipped == version || u.DecisionVersion == version {
			return nil
		}
		d := Decision{ID: uid(), Kind: DecisionUpgradeAvailable, Title: version + " is available", Context: fmt.Sprintf("Running v%s; %s is available.\n\n%s\n\nRelease: %s\n\nUpgrade by hand: brew upgrade %s\nThen restart crew-assistant. For a standalone install, download the release by hand.", trimVersion(running), version, result.Notes, result.URL, cfg.Formula), Recommendation: "Upgrade by hand when you're ready.", Choices: []string{ChoiceUpgradeByHand, "Skip " + version}, Status: DecisionOpen, CreatedAt: now}
		v.Decisions = append(v.Decisions, d)
		u.DecisionVersion = version
		record(v, now, "", "decision.opened", d.Title)
		return nil
	})
}
