package core

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// RouteAsset durably returns a deciding task to its implementer for a
// production spec. It preserves the draft and session and leaves the report
// pending until AskDesign records the production request atomically.
func (s *Service) RouteAsset(ctx context.Context, expected Task, u Unreachable, reporter string, briefVersions ...int) (Task, error) {
	return s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if (t.Status != TaskDeciding && !(t.Status == TaskLanding && u.Source == "landing")) || !sameAssetRecord(*t, expected) || !assetBriefCurrent(v, *t, firstVersion(briefVersions)) {
			return ErrConflict
		}
		if _, ok := t.Designer(); !ok {
			return fmt.Errorf("this task's team has no designer: %w", ErrConflict)
		}
		if u.Criterion == "" {
			u.Criterion = u.Why
		}
		p := project(v, t.ProjectID)
		i := slices.IndexFunc(t.Unreachable, func(r Unreachable) bool {
			return ReportKey(r) == ReportKey(u) && r.Revision == u.Revision
		})
		if i < 0 && u.Source != "review" && u.Source != "landing" {
			return ErrConflict
		}
		if obsoleteAssetReport(*t, p, u) {
			t.Unreachable = slices.DeleteFunc(t.Unreachable, func(r Unreachable) bool { return ReportKey(r) == ReportKey(u) })
			recordTask(v, s.now().UTC(), t, "task.asset_obsolete", u.Criterion+": requirement removed before routing")
			return nil
		}
		u.TextVersion = t.TextVersion
		if p != nil {
			u.BriefVersion = p.Brief.Version
		}
		if slices.Contains(t.Criteria, u.Criterion) {
			u.Bound = "task"
		} else if p != nil && slices.Contains(p.Brief.Criteria, u.Criterion) {
			u.Bound = "brief"
		}
		if i >= 0 && t.Unreachable[i].Why != u.Why && u.Source != "review" {
			return ErrConflict
		}
		if i >= 0 && u.AssetCreation == nil {
			u.AssetCreation = t.Unreachable[i].AssetCreation
		}
		u.Routed = reporter + ": asset creation belongs to the team's designer; implementer must supply a production spec (or correct classification)"
		if u.AssetCreation == nil {
			u.Routed = reporter + ": classify blocked work before owner escalation; asset creation belongs to the designer"
		}
		if i < 0 {
			t.Unreachable = append(t.Unreachable, u)
		} else {
			t.Unreachable[i] = u
		}
		t.Status, t.Detail = TaskWriting, u.Routed
		now := s.now().UTC()
		t.UpdatedAt = now
		recordTask(v, now, t, "task.asset_routed", u.Criterion+": "+u.Routed)
		derive(v, t)
		return nil
	})
}

func sameAssetRecord(t, expected Task) bool {
	return t.TextVersion == expected.TextVersion && reflect.DeepEqual(t.Revisions, expected.Revisions) && reflect.DeepEqual(t.Unreachable, expected.Unreachable) && reflect.DeepEqual(t.Design, expected.Design)
}

// RetainLandingAssets records readable PM obstacles before another correction,
// or routes the entire pending set together. Omitted reports remain durable.
func (s *Service) RetainLandingAssets(ctx context.Context, expected Task, reports []Unreachable, reporter string, route bool, briefVersion int) (Task, error) {
	return s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if (t.Status != TaskDeciding && t.Status != TaskLanding) || !sameAssetRecord(*t, expected) || !assetBriefCurrent(v, *t, briefVersion) {
			return ErrConflict
		}
		if _, ok := t.Designer(); !ok {
			return ErrConflict
		}
		for _, u := range reports {
			if u.Source != "landing" || u.ID == "" || u.Criterion == "" || u.Why == "" {
				return ErrConflict
			}
			i := slices.IndexFunc(t.Unreachable, func(old Unreachable) bool { return ReportKey(old) == ReportKey(u) })
			if i < 0 {
				if u.AssetCreation == nil || *u.AssetCreation {
					if slices.Contains(t.Criteria, u.Criterion) {
						u.Bound = "task"
					} else if p := project(v, t.ProjectID); p != nil && slices.Contains(p.Brief.Criteria, u.Criterion) {
						u.Bound = "brief"
					}
					t.Unreachable = append(t.Unreachable, u)
				}
			} else if t.Unreachable[i].Source != "landing" || t.Unreachable[i].Criterion != u.Criterion {
				return ErrConflict
			} else if u.AssetCreation != nil {
				t.Unreachable[i].AssetCreation = u.AssetCreation
				if !*u.AssetCreation {
					t.Unreachable[i].Routed = ""
				}
			}
		}
		if route {
			routeLandingAssets(t, project(v, t.ProjectID), reporter)
		}
		t.UpdatedAt = s.now().UTC()
		recordTask(v, s.now().UTC(), t, "task.landing_assets", reporter+": retained asset obstacle evidence")
		derive(v, t)
		return nil
	})
}

// routeLandingAssets recovers evidence retained before a lost correction turn.
func routeLandingAssets(t *Task, p *Project, reporter string) {
	if _, ok := t.Designer(); !ok {
		return
	}
	t.Unreachable = slices.DeleteFunc(t.Unreachable, func(u Unreachable) bool {
		if u.Source == "landing" && obsoleteAssetReport(*t, p, u) {
			t.KeepSettledEvidence(u)
			return true
		}
		return false
	})
	for i := range t.Unreachable {
		u := &t.Unreachable[i]
		if u.Source != "landing" || u.AssetCreation != nil && !*u.AssetCreation || obsoleteAssetReport(*t, p, *u) {
			continue
		}
		u.Routed = reporter + ": supply a production spec or classify this report explicitly"
		t.Status, t.Detail = TaskWriting, u.Routed
	}
}

// ReportKey distinguishes review findings even when they quote the same criterion.
func ReportKey(u Unreachable) string {
	if u.ID != "" {
		return u.ID
	}
	return u.Criterion
}

// NeedsLandingAssetReply survives a lost PM correction conversation.
func (t Task) NeedsLandingAssetReply() bool {
	if _, designer := t.Designer(); !designer {
		return false
	}
	return slices.ContainsFunc(t.Unreachable, func(u Unreachable) bool {
		return u.Source == "landing" && (u.AssetCreation == nil || *u.AssetCreation)
	})
}

func reportFor(reports []Unreachable, key string) *Unreachable {
	for i := range reports {
		if ReportKey(reports[i]) == key {
			return &reports[i]
		}
	}
	// A correction quoting a criterion addresses its active route, never an
	// earlier classification of another finding against that criterion.
	for i := range reports {
		if reports[i].Criterion == key && reports[i].Routed != "" {
			return &reports[i]
		}
	}
	return nil
}

func firstVersion(versions []int) int {
	if len(versions) > 0 {
		return versions[0]
	}
	return 0
}

func assetBriefCurrent(v *Snapshot, t Task, version int) bool {
	p := project(v, t.ProjectID)
	return version == 0 || p != nil && p.Brief.Version == version
}

func obsoleteAssetReport(t Task, p *Project, u Unreachable) bool {
	// Owner assignments supersede inherited team requirements.
	if !teamRequirement(p, t.Criteria, t.OwnerChecks, t.OwnerSteps, t.OwnerTook, u.Criterion) && (slices.Contains(t.OwnersAlready(), u.Criterion) || slices.Contains(t.OwnerTook, u.Criterion)) {
		return true
	}
	// Bindings and edit history establish origin, not present authority.
	// An identical requirement in either source (including one restored by
	// undo) still requires the work.
	if slices.Contains(t.Criteria, u.Criterion) || p != nil && slices.Contains(p.Brief.Criteria, u.Criterion) {
		return false
	}
	if u.Bound == "task" && !slices.Contains(t.Criteria, u.Criterion) ||
		u.Bound == "brief" && p != nil && !slices.Contains(p.Brief.Criteria, u.Criterion) {
		return true
	}
	// Report versions precede the redirect; do not restamp them before
	// checking the edits that removed their originating requirement.
	for i, e := range t.Edits {
		if i >= u.TextVersion && slices.Contains(e.Before.Criteria, u.Criterion) && !slices.Contains(e.After.Criteria, u.Criterion) {
			return true
		}
	}
	return false
}

// MergeReports updates explicit corrections without losing the originating
// review finding, routing reason, or reports omitted from a partial reply.
func MergeReports(previous, corrections []Unreachable) []Unreachable {
	out := slices.Clone(previous)
	for _, u := range corrections {
		if old := reportFor(out, ReportKey(u)); old != nil {
			old.AssetCreation, old.Why = u.AssetCreation, u.Why
			if u.AssetCreation != nil && !*u.AssetCreation {
				old.Routed = ""
			}
		} else {
			out = append(out, u)
		}
	}
	return out
}

// RecordAssetClassification ends a correction turn against the existing
// draft. It never snapshots, publishes, lands, or changes the round.
func (s *Service) RecordAssetClassification(ctx context.Context, expected Task, reports []Unreachable, writer string, session []byte, briefVersions ...int) (Task, error) {
	return s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if t.Status != TaskWriting || len(t.Revisions) == 0 || !sameAssetRecord(*t, expected) || !assetBriefCurrent(v, *t, firstVersion(briefVersions)) {
			return ErrConflict
		}
		if len(reports) == 0 {
			return fmt.Errorf("classification correction needs reports")
		}
		for _, u := range reports {
			if _, designer := t.Designer(); designer && u.AssetCreation == nil {
				return fmt.Errorf("classification correction needs asset_creation")
			}
		}
		if err := t.CheckLinkedAssetClassifications(reports); err != nil {
			return err
		}
		t.Unreachable = t.WithoutLinkedAssetReports(MergeReports(t.Unreachable, reports))
		for _, u := range reports {
			if corrected := reportFor(t.Unreachable, ReportKey(u)); corrected != nil {
				corrected.TextVersion = t.TextVersion
				corrected.Revision = len(t.Revisions)
			}
		}
		for i := range t.Unreachable {
			if t.Unreachable[i].Source != "review" {
				t.Unreachable[i].Revision = len(t.Revisions)
			}
		}
		if seat, ok := t.Role(writer); ok && len(session) > 0 {
			t.KeepThread(RoleImplementer, seat, session)
		}
		t.Status, t.Detail = TaskDeciding, "Classification corrected against the existing draft"
		if t.NeedsAssetIntegration() {
			t.Status, t.Detail = TaskWriting, "Integrate delivered assets and provenance in a new draft"
		}
		t.Failures, t.RetryAt = 0, time.Time{}
		now := s.now().UTC()
		t.UpdatedAt = now
		recordTask(v, now, t, "task.asset_classified", writer+": "+t.Detail)
		derive(v, t)
		return nil
	})
}

// PrepareAssetWriting reconciles durable reports before prompting a writer,
// including after a restart or an owner changes the task's team.
func (s *Service) PrepareAssetWriting(ctx context.Context, expected Task) (Task, error) {
	return s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if t.Status != TaskWriting || t.Delivering != nil || t.PRMergePending() || !sameAssetRecord(*t, expected) {
			return ErrConflict
		}
		p := project(v, t.ProjectID)
		now := s.now().UTC()
		routeLandingAssets(t, p, "PM")
		t.Unreachable = slices.DeleteFunc(t.Unreachable, func(u Unreachable) bool {
			if !u.NeedsAssetReply() {
				return false
			}
			obsolete := obsoleteAssetReport(*t, p, u)
			if obsolete {
				recordTask(v, now, t, "task.asset_obsolete", u.Criterion+": routed report removed after requirement edit")
			}
			return obsolete
		})
		if _, ok := t.Designer(); !ok {
			missing, classification := false, false
			for i := range t.Unreachable {
				u := &t.Unreachable[i]
				if !u.NeedsAssetReply() {
					continue
				}
				if u.AssetCreation == nil || !*u.AssetCreation {
					u.Routed = ""
					// Unknown review findings remain review findings. Without a
					// designer the original round-limit decision can proceed.
					classification = true
					continue
				}
				u.Routed, u.Source = "", ""
				u.Why = "The task's current team has no designer to fulfil the recorded asset requirement. " + u.Why
				u.Revision = len(t.Revisions)
				missing = true
			}
			if classification && len(t.Revisions) > 0 {
				t.Status, t.Detail = TaskDeciding, "Classification request ended; the original report remains for its decision"
				recordTask(v, now, t, "task.asset_classification_ended", t.Detail)
			}
			if missing {
				t.Detail = "The task's current team has no designer for its asset requirement"
				if len(t.Revisions) > 0 {
					t.Status = TaskDeciding
				}
				recordTask(v, now, t, "task.asset_capability_missing", t.Detail)
			}
		}
		derive(v, t)
		return nil
	})
}

// RestoreUnfinishedProduction returns linked obstacles when a production seat
// disappears. Completed groups remain on the request for integration/recovery.
func (s *Service) RestoreUnfinishedProduction(ctx context.Context, expected Task) (Task, error) {
	var written, removed []string
	out, err := s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if t.Status != TaskDesigning || !sameAssetRecord(*t, expected) {
			return ErrConflict
		}
		if _, ok := t.Designer(); ok {
			return ErrConflict
		}
		r := t.OpenDesign()
		if r == nil || r.Production == nil {
			return ErrConflict
		}
		now := s.now().UTC()
		t.Attachments = slices.DeleteFunc(t.Attachments, func(a Attachment) bool {
			if a.Design != r.ID || a.Asset == "" {
				return false
			}
			if slices.ContainsFunc(r.Production.Turns, func(turn ProductionTurn) bool { return turn.N == a.Turn && turn.DoneAt.IsZero() }) {
				path, _ := s.attachmentPath(t.ID, a.ID)
				removed = append(removed, path)
				return true
			}
			return false
		})
		if len(r.Production.Delivered) > 0 && r.Production.Provenance == "" {
			if err := s.keepProductionHandoff(t, r, r.Designer, now, &written); err != nil {
				return err
			}
		}
		p := project(v, t.ProjectID)
		for _, u := range r.AssetReports {
			if obsoleteAssetReport(*t, p, u) {
				t.KeepSettledEvidence(u)
			} else {
				t.Unreachable = MergeReports(t.Unreachable, []Unreachable{u})
			}
		}
		r.AnsweredAt = s.now().UTC()
		r.Input = "Production interrupted: the task's current team has no designer; delivered groups are retained"
		t.Status, t.Detail = TaskWriting, r.Input
		recordTask(v, s.now().UTC(), t, "task.asset_production_interrupted", r.Input)
		derive(v, t)
		return nil
	})
	if err != nil {
		removeAll(written)
	} else {
		removeAll(removed)
	}
	return out, err
}

// Suspend inactive obligations reversibly; completed integration stays settled.
func reconcileAssetIntegration(t *Task, p *Project) {
	for i := range t.Design {
		r := &t.Design[i]
		if (!r.IntegrationPending && !r.IntegrationSuspended) || len(r.AssetReports) == 0 {
			continue
		}
		active := slices.ContainsFunc(r.AssetReports, func(u Unreachable) bool { return !obsoleteAssetReport(*t, p, u) })
		r.IntegrationPending, r.IntegrationSuspended = active, !active
	}
	if t.NeedsAssetIntegration() && (t.Status == TaskDeciding || t.Status == TaskLanding || t.Status == TaskAwaiting && t.PROpen()) && t.Delivering == nil && !t.PRMergePending() {
		t.Status, t.Detail = TaskWriting, "Integrate delivered assets and provenance in a new draft"
	}
}

// RouteReviewAssets persists the full classification set atomically.
func (s *Service) RouteReviewAssets(ctx context.Context, expected Task, reports []Unreachable, reporter string, briefVersion int) (Task, error) {
	return s.editTaskRecord(ctx, "", expected.ID, func(t *Task, v *Snapshot) error {
		if t.Status != TaskDeciding || !sameAssetRecord(*t, expected) || !assetBriefCurrent(v, *t, briefVersion) {
			return ErrConflict
		}
		if _, ok := t.Designer(); !ok {
			return ErrConflict
		}
		p := project(v, t.ProjectID)
		stored := false
		for _, u := range reports {
			if u.Source != "review" || u.ID == "" || u.AssetCreation == nil {
				return ErrConflict
			}
			if old := reportFor(t.Unreachable, ReportKey(u)); old != nil {
				u.Bound = old.Bound
			}
			if obsoleteAssetReport(*t, p, u) {
				t.Unreachable = slices.DeleteFunc(t.Unreachable, func(old Unreachable) bool { return ReportKey(old) == ReportKey(u) })
				t.KeepSettledEvidence(u)
				recordTask(v, s.now().UTC(), t, "task.asset_obsolete", u.Criterion+": requirement removed before review routing")
				continue
			}
			if slices.Contains(t.Criteria, u.Criterion) {
				u.Bound = "task"
			} else if p != nil && slices.Contains(p.Brief.Criteria, u.Criterion) {
				u.Bound = "brief"
			}
			u.Routed = ""
			if *u.AssetCreation {
				u.Routed = reporter + ": supply a production spec or correct this classification"
				t.Status, t.Detail = TaskWriting, u.Routed
			}
			if old := reportFor(t.Unreachable, ReportKey(u)); old != nil {
				*old = u
			} else {
				t.Unreachable = append(t.Unreachable, u)
			}
			stored = true
		}
		t.UpdatedAt = s.now().UTC()
		if stored {
			recordTask(v, t.UpdatedAt, t, "task.review_assets", reporter+": recorded review classifications")
		}
		derive(v, t)
		return nil
	})
}

// WithoutLinkedAssetReports prevents a linked hand-off from being reopened.
// ID-less writer reports match only their original revision and explanation;
// another draft can report a new obstacle against the same requirement.
func (t Task) WithoutLinkedAssetReports(reports []Unreachable) []Unreachable {
	return slices.DeleteFunc(slices.Clone(reports), func(u Unreachable) bool { return t.LinkedAssetReport(u) != nil })
}

// LinkedAssetReport identifies an obstacle already covered by finished production.
// ID-less reports must retain their original revision and explanation.
func (t Task) LinkedAssetReport(u Unreachable) *Unreachable {
	for _, d := range t.Design {
		if d.Production == nil || d.Open() || len(d.Production.Remaining()) != 0 {
			continue
		}
		for _, old := range d.AssetReports {
			if u.ID != "" && old.ID == u.ID || u.ID == "" && old.ID == "" && old.Criterion == u.Criterion && old.Revision == u.Revision && old.Why == u.Why {
				return &old
			}
		}
	}
	return nil
}

// CheckLinkedAssetClassifications refuses to silently discard a contradictory
// correction of a fulfilled obstacle. New findings need their own identity.
func (t Task) CheckLinkedAssetClassifications(reports []Unreachable) error {
	for _, u := range reports {
		if old := t.LinkedAssetReport(u); old != nil && old.AssetCreation != nil && u.AssetCreation != nil && *old.AssetCreation != *u.AssetCreation {
			return fmt.Errorf("report %s is already covered by production; report a new obstacle separately instead of changing its classification", ReportKey(u))
		}
	}
	return nil
}
