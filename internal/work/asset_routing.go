package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

// assetRule is shared by implementers and PMs, and only offered to teams
// with the production hand-off capability.
func assetRule(t core.Task) string {
	if _, ok := t.Designer(); !ok {
		return ""
	}
	rule := "\nThe implementer changes code, including animation behaviour, timing, sprite loading and image decoding. The designer creates bitmap and other generated assets. Asset creation unavailable in the implementer's sandbox is not outside the team's reach: ask the designer through the production hand-off with a spec giving names/count, format, dimensions, references and generation constraints. Copy finished assets and provenance.json into the workspace for the task's recorded commit, and request revisions through the same hand-off as often as needed. Ask with only a production block: one named asset per line (name: what it is), at most 100, with optional notes after a blank line. When covering particular pending reports, add Requirement: followed by each exact quoted requirement on separate note lines. With a single routed report a production-only response covers that report; with multiple reports name the ones the spec covers. Classification-only corrections use an owner-step block and leave the existing draft intact. After a production hand-back, remain in writing until a new draft integrates delivered assets and provenance; classification alone cannot approve the old draft. Ordinary design input remains limited; production revisions are not. Classify blocked work, not incidental words: include asset_creation: true or false in each owner-step entry and each PM finding or owner-step judgement. Missing classification must be corrected; asset_creation true takes precedence over owner_step true.\n"
	for _, u := range t.Unreachable {
		if u.NeedsAssetReply() {
			rule += "\nReport ID (include report_id in a correction): " + u.ID + "\nRequired before another draft: " + u.Criterion + "\nReported obstacle: " + u.Why + "\nRouting reason: " + u.Routed + "\nReply with only a production block, or correct an unclassified report with an explicitly classified owner-step entry quoting this requirement.\n"
		}
	}
	return rule
}

func (lp *Loop) routeAsset(ctx context.Context, p core.Project, t core.Task, u core.Unreachable, reporter string) error {
	_, err := lp.Core.RouteAsset(ctx, t, u, reporter, p.Brief.Version)
	if errors.Is(err, core.ErrConflict) {
		return nil
	}
	return err
}

// writerAssetError runs before a snapshot. It never guesses from vocabulary.
func writerAssetError(t core.Task, in writerReply) error {
	if _, ok := t.Designer(); !ok {
		return nil
	}
	if strings.TrimSpace(in.unmet) != "" {
		entries, err := ownerStepEntries(in.unmet)
		if err != nil {
			return errors.New("owner-step needs valid JSON and asset_creation classification")
		}
		for _, e := range entries {
			linked := t.LinkedAssetReport(core.Unreachable{ID: e.ReportID})
			if linked != nil && matchCriterion([]string{linked.Criterion}, e.Requirement) == linked.Criterion {
				if e.AssetCreation == nil {
					return errors.New("classify each blocked requirement with asset_creation true or false")
				}
				if err := t.CheckLinkedAssetClassifications([]core.Unreachable{{ID: e.ReportID, AssetCreation: e.AssetCreation}}); err != nil {
					return err
				}
				// Repeating settled evidence is harmless. An explicit production
				// spec can still request a revision without reopening this report.
				continue
			}
			if e.ReportID != "" && !slices.ContainsFunc(t.Unreachable, func(u core.Unreachable) bool {
				return u.ID == e.ReportID && matchCriterion([]string{u.Criterion}, e.Requirement) == u.Criterion
			}) {
				return errors.New("report_id must identify the quoted recorded report")
			}
			if e.AssetCreation == nil {
				// A partial spec may leave another report unclassified. Keep
				// it pending; production acknowledges only its named coverage.
				if len(in.assets) > 0 && len(in.requirements) > 0 && !productionCovers(in, e) {
					continue
				}
				return errors.New("classify each blocked requirement with asset_creation true or false; asset creation needs a production block")
			}
			if *e.AssetCreation && len(in.assets) == 0 {
				return fmt.Errorf("asset creation belongs to the designer: supply a production block for %s", e.Requirement)
			}
		}
	}
	if len(in.assets) > 0 {
		entries, _ := ownerStepEntries(in.unmet)
		potential := map[string]bool{}
		for _, e := range entries {
			if e.AssetCreation == nil || *e.AssetCreation {
				key := e.Requirement
				if e.ReportID != "" {
					key = e.ReportID
				}
				for _, u := range t.Unreachable {
					if e.ReportID == u.ID && e.ReportID != "" || e.ReportID == "" && matchCriterion([]string{u.Criterion}, e.Requirement) == u.Criterion {
						key = core.ReportKey(u)
						break
					}
				}
				potential[key] = true
			}
		}
		for _, u := range t.Unreachable {
			if u.NeedsAssetReply() {
				if slices.ContainsFunc(entries, func(e ownerStepEntry) bool {
					return e.AssetCreation != nil && !*e.AssetCreation && (e.ReportID == "" || e.ReportID == u.ID) && matchCriterion([]string{u.Criterion}, e.Requirement) == u.Criterion
				}) {
					continue
				}
				potential[core.ReportKey(u)] = true
			}
		}

		candidates := slices.Clone(entries)
		for _, u := range t.Unreachable {
			if u.NeedsAssetReply() && !slices.ContainsFunc(candidates, func(e ownerStepEntry) bool { return entryKey(e) == core.ReportKey(u) }) {
				candidates = append(candidates, ownerStepEntry{ReportID: u.ID, Requirement: u.Criterion, AssetCreation: u.AssetCreation})
			}
		}
		for _, requirement := range in.requirements {
			if _, err := resolveCoverage(candidates, requirement); err != nil {
				return err
			}
		}
		if len(in.requirements) == 0 && len(potential) > 1 {
			return errors.New("with multiple pending reports, name the covered requirements in production notes")
		}
		return nil
	}
	for _, u := range t.WithoutLinkedAssetReports(t.Unreachable) {
		if u.NeedsAssetReply() {
			entries, _ := ownerStepEntries(in.unmet)
			matched := false
			for _, e := range entries {
				if (e.ReportID == "" || e.ReportID == u.ID) && matchCriterion([]string{u.Criterion}, e.Requirement) == u.Criterion && e.AssetCreation != nil && !*e.AssetCreation {
					matched = true
				}
			}
			if matched {
				continue
			}
			return errors.New("classify the routed requirement or request production before another draft")
		}
	}
	return nil
}

func hasProductionDesigner(t core.Task) bool { _, ok := t.Designer(); return ok }

// mergeWriterReports treats a correction as a partial update, never as a
// replacement of the original reports. A production-only correction accepts
// the single unclassified obstacle it was asked to supply a spec for; with
// several unclassified obstacles the reporter must disambiguate explicitly.
func mergeWriterReports(t core.Task, previous string, in writerReply) (string, error) {
	var entries, corrected []ownerStepEntry
	if previous != "" {
		var err error
		entries, err = readableOwnerSteps(previous)
		if err != nil {
			return previous, err
		}
	}
	if in.unmet != "" {
		var err error
		corrected, err = ownerStepEntries(in.unmet)
		if err != nil {
			if !hasProductionDesigner(t) {
				return in.unmet, nil
			}
			return in.unmet, errors.New("owner-step needs a JSON list of reports")
		}
	}
	for _, e := range corrected {
		var quotes []string
		for _, old := range entries {
			quotes = append(quotes, old.Requirement)
		}
		quoted := matchCriterion(quotes, e.Requirement)
		i := slices.IndexFunc(entries, func(old ownerStepEntry) bool {
			return (e.ReportID != "" && old.ReportID == e.ReportID) || (e.ReportID == "" && old.Requirement == quoted)
		})
		if i < 0 {
			entries = append(entries, e)
		} else {
			e.Requirement = entries[i].Requirement
			entries[i] = e
		}
	}
	if len(in.assets) > 0 {
		candidates := slices.Clone(entries)
		for _, u := range t.Unreachable {
			if u.NeedsAssetReply() && !slices.ContainsFunc(candidates, func(e ownerStepEntry) bool { return entryKey(e) == core.ReportKey(u) }) {
				candidates = append(candidates, ownerStepEntry{ReportID: u.ID, Requirement: u.Criterion, AssetCreation: u.AssetCreation})
			}
		}
		covered := map[string]bool{}
		for _, quote := range in.requirements {
			key, err := resolveCoverage(candidates, quote)
			if err != nil {
				// The correction turn still owes every readable obstacle
				// from this reply, even when its coverage cannot be used.
				if len(entries) > 0 {
					data, _ := json.Marshal(entries)
					return string(data), err
				}
				return "", err
			}
			covered[key] = true
		}
		potential := 0
		for _, e := range entries {
			if e.AssetCreation == nil || *e.AssetCreation {
				potential++
			}
		}
		if previous != "" {
			for i := range entries {
				if entries[i].AssetCreation == nil && (covered[entryKey(entries[i])] || len(in.requirements) == 0 && potential == 1) {
					yes := true
					entries[i].AssetCreation = &yes
				}
			}
		}
		// Only an unambiguous production-only response implies a link to a
		// durable route. Otherwise use named Requirement notes or reports.
		var routed []core.Unreachable
		for _, u := range t.Unreachable {
			if u.NeedsAssetReply() {
				routed = append(routed, u)
			}
		}
		for _, u := range routed {
			named := covered[core.ReportKey(u)]
			if !named && !(len(in.requirements) == 0 && len(routed) == 1 && len(entries) == 0) {
				continue
			}
			if slices.ContainsFunc(entries, func(e ownerStepEntry) bool {
				return e.ReportID == u.ID && e.ReportID != "" || e.ReportID == "" && u.ID == "" && matchCriterion([]string{u.Criterion}, e.Requirement) == u.Criterion
			}) {
				continue
			}
			yes := true
			entries = append(entries, ownerStepEntry{ReportID: u.ID, Requirement: u.Criterion, Why: u.Why, AssetCreation: &yes})
		}
	}
	if len(entries) == 0 {
		return "", nil
	}
	data, err := json.Marshal(entries)
	return string(data), err
}

func productionLinks(in writerReply, reports []core.Unreachable) []string {
	if len(in.assets) == 0 {
		return nil
	}
	var linked []string
	for _, u := range reports {
		if u.AssetCreation != nil && *u.AssetCreation && (len(in.requirements) == 0 || slices.ContainsFunc(in.requirements, func(c string) bool {
			return coverageKey(reports, c) == core.ReportKey(u)
		})) {
			linked = append(linked, core.ReportKey(u))
		}
	}
	return linked
}

// ownerStepEntries preserves the existing single-object reply contract.
func ownerStepEntries(block string) ([]ownerStepEntry, error) {
	var entries []ownerStepEntry
	if err := json.Unmarshal([]byte(block), &entries); err == nil {
		return entries, nil
	}
	var one ownerStepEntry
	if err := json.Unmarshal([]byte(block), &one); err != nil {
		return nil, err
	}
	return []ownerStepEntry{one}, nil
}

// bindWriterReports records the requirement version before a later redirect.
// Review identity comes from the durable route, rather than the correction's
// clipped quote or newly worded explanation.
func bindWriterReports(p core.Project, t core.Task, reports []core.Unreachable) []core.Unreachable {
	for i := range reports {
		u := &reports[i]
		u.TextVersion, u.BriefVersion = t.TextVersion, p.Brief.Version
		if slices.Contains(t.Criteria, u.Criterion) {
			u.Bound = "task"
		} else if slices.Contains(p.Brief.Criteria, u.Criterion) {
			u.Bound = "brief"
		}
		for _, old := range t.Unreachable {
			if u.ID != "" && u.ID == old.ID || u.ID == "" && old.Routed != "" && old.Criterion == u.Criterion {
				u.ID, u.Source, u.Finding, u.Bound = old.ID, old.Source, old.Finding, old.Bound
				break
			}
		}
	}
	return reports
}

func productionCovers(in writerReply, e ownerStepEntry) bool {
	return e.ReportID != "" && slices.Contains(in.requirements, e.ReportID) || slices.ContainsFunc(in.requirements, func(c string) bool { return matchCriterion([]string{c}, e.Requirement) == c })
}

// readableOwnerSteps retains individually readable obstacles during bounded
// correction even when a sibling's classification has the wrong JSON type.
func readableOwnerSteps(block string) ([]ownerStepEntry, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		raw = []json.RawMessage{json.RawMessage(block)}
	}
	var entries []ownerStepEntry
	for _, item := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, err
		}
		classification := fields["asset_creation"]
		delete(fields, "asset_creation")
		data, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		var e ownerStepEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		if strings.TrimSpace(e.Requirement) == "" || strings.TrimSpace(e.Why) == "" {
			return nil, errors.New("repair every original owner-step report before requesting production")
		}
		var classified *bool
		if json.Unmarshal(classification, &classified) == nil {
			e.AssetCreation = classified
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func entryKey(e ownerStepEntry) string {
	if e.ReportID != "" {
		return e.ReportID
	}
	return e.Requirement
}

// Shortened quotes must resolve against the complete candidate set.
func resolveCoverage(entries []ownerStepEntry, quote string) (string, error) {
	quote = strings.TrimSpace(quote)
	matches := map[string]bool{}
	for _, e := range entries {
		if e.AssetCreation != nil && !*e.AssetCreation {
			continue
		}
		if quote != "" && quote == entryKey(e) {
			matches[entryKey(e)] = true
		}
	}
	if len(matches) == 0 {
		for _, e := range entries {
			if e.AssetCreation != nil && !*e.AssetCreation {
				continue
			}
			if quote != "" && matchCriterion([]string{e.Requirement}, quote) == e.Requirement {
				matches[entryKey(e)] = true
			}
		}
	}
	if len(matches) != 1 {
		return "", errors.New("production Requirement notes must identify exactly one pending asset report; use report_id or the full requirement")
	}
	for key := range matches {
		return key, nil
	}
	return "", nil
}
func coverageKey(reports []core.Unreachable, quote string) string {
	var entries []ownerStepEntry
	for _, u := range reports {
		entries = append(entries, ownerStepEntry{ReportID: u.ID, Requirement: u.Criterion, AssetCreation: u.AssetCreation})
	}
	key, _ := resolveCoverage(entries, quote)
	return key
}

// routedWriterReports preserves kept team asset work, including its identity.
func routedWriterReports(p core.Project, t core.Task, block string, production ...writerReply) []core.Unreachable {
	reports, kept := parseOwnerSteps(block, len(t.Revisions), append(slices.Clone(t.Criteria), p.Brief.Criteria...), t.OwnersAlready(), t.TeamKept)
	for _, u := range kept {
		covered := len(production) > 0 && len(production[0].assets) > 0 && u.AssetCreation != nil && *u.AssetCreation && (len(production[0].requirements) == 0 || productionCovers(production[0], ownerStepEntry{ReportID: u.ID, Requirement: u.Criterion}))
		if covered || slices.ContainsFunc(t.Unreachable, func(old core.Unreachable) bool {
			return old.Routed != "" && (u.ID != "" && u.ID == old.ID || u.ID == "" && u.Criterion == old.Criterion)
		}) {
			reports = append(reports, u)
		}
	}
	return t.WithoutLinkedAssetReports(bindWriterReports(p, t, reports))
}

func writerAssetClassificationError(p core.Project, t core.Task, block string) error {
	reports, kept := parseOwnerSteps(block, len(t.Revisions), append(slices.Clone(t.Criteria), p.Brief.Criteria...), t.OwnersAlready(), t.TeamKept)
	return t.CheckLinkedAssetClassifications(bindWriterReports(p, t, append(reports, kept...)))
}

func (lp *Loop) assetClassificationFailed(ctx context.Context, t core.Task, writer string, cause error) error {
	_, err := lp.Core.AddNote(ctx, core.NoteInput{Project: t.ProjectID, Task: t.ID, By: writer, Kind: core.RoleImplementer, While: core.TaskWriting, Text: cause.Error()})
	if err != nil && !errors.Is(err, core.ErrConflict) {
		return err
	}
	return lp.roleFailed(ctx, t, writer, cause)
}
