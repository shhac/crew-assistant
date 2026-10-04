package core

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/shhac/crew-assistant/internal/text"
)

// Production limits are independent of ordinary notes and design input.
// A hundred small assets covers frame sets without making attachments unbounded.
const (
	MaxProductionAssets = 100
	MaxProductionBytes  = 200 << 20
	MaxRejectedBytes    = 50 << 20
	// Keeps the final provenance JSON below the ordinary per-file limit.
	MaxAssetProvenanceBytes = 32 << 10
)

type WantedAsset struct {
	Name string `json:"name"`
	Want string `json:"want"`
}
type ProductionTurn struct {
	N         int       `json:"n"`
	Designer  string    `json:"designer"`
	StartedAt time.Time `json:"started_at"`
	DoneAt    time.Time `json:"done_at,omitzero"`
	Delivered []string  `json:"delivered"`
}
type DeliveredAsset struct {
	Asset      string         `json:"asset"`
	Attachment string         `json:"attachment"`
	Turn       int            `json:"turn"`
	SHA256     string         `json:"sha256"`
	Size       int64          `json:"size"`
	Prompt     string         `json:"prompt"`
	Generator  string         `json:"generator"`
	Settings   map[string]any `json:"settings"`
	References []string       `json:"references"`
}
type Production struct {
	Assets     []WantedAsset    `json:"assets"`
	Turns      []ProductionTurn `json:"turns"`
	Delivered  []DeliveredAsset `json:"delivered"`
	Archive    string           `json:"archive,omitempty"`
	Provenance string           `json:"provenance,omitempty"`
}

func (p Production) Remaining() []WantedAsset {
	var out []WantedAsset
	for _, a := range p.Assets {
		if !slices.ContainsFunc(p.Delivered, func(d DeliveredAsset) bool { return d.Asset == a.Name }) {
			out = append(out, a)
		}
	}
	return out
}
func validateWanted(assets []WantedAsset) error {
	if len(assets) == 0 || len(assets) > MaxProductionAssets {
		return fmt.Errorf("production needs 1 to %d named assets", MaxProductionAssets)
	}
	seen := map[string]bool{}
	for _, a := range assets {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Want) == "" || len(a.Name) > 120 || len(a.Want) > 4000 || seen[a.Name] {
			return errors.New("each production asset needs a unique name and a description (name at most 120 bytes, description at most 4000 bytes)")
		}
		seen[a.Name] = true
	}
	return nil
}
func productionRequest(t *Task, request string, turn int) (*DesignRequest, error) {
	r := t.OpenDesign()
	if t.Status != TaskDesigning || r == nil || r.ID != request || r.Production == nil || len(r.Production.Turns) == 0 {
		return nil, ErrConflict
	}
	last := r.Production.Turns[len(r.Production.Turns)-1]
	if last.N != turn || !last.DoneAt.IsZero() {
		return nil, ErrConflict
	}
	return r, nil
}

// StartProductionTurn discards only asset files from unfinished turns.
// Rejected variants are history and stay in the archive.
func (s *Service) StartProductionTurn(ctx context.Context, taskID, requestID, designer string) (Task, error) {
	var removed []string
	out, err := s.editTaskRecord(ctx, "", taskID, func(t *Task, v *Snapshot) error {
		r := t.OpenDesign()
		if t.Status != TaskDesigning || r == nil || r.ID != requestID || r.Production == nil {
			return ErrConflict
		}
		p := r.Production
		unfinished := map[int]bool{}
		for _, turn := range p.Turns {
			if turn.DoneAt.IsZero() {
				unfinished[turn.N] = true
			}
		}
		t.Attachments = slices.DeleteFunc(t.Attachments, func(a Attachment) bool {
			if a.Design == r.ID && a.Asset != "" && unfinished[a.Turn] {
				path, _ := s.attachmentPath(t.ID, a.ID)
				removed = append(removed, path)
				return true
			}
			return false
		})
		now := s.now().UTC()
		p.Turns = append(p.Turns, ProductionTurn{N: len(p.Turns) + 1, Designer: designer, StartedAt: now})
		if len(removed) > 0 {
			recordTask(v, now, t, "task.production_dropped", fmt.Sprintf("dropped %d files from an unfinished turn", len(removed)))
		}
		t.UpdatedAt = now
		return nil
	})
	if err == nil {
		removeAll(removed)
	}
	return out, err
}

// ValidateProduction checks a whole group before any asset is recorded as delivered.
// The loop also calls it while asking for JSON, so malformed groups can be retried.
func (s *Service) ValidateProduction(ctx context.Context, taskID, requestID string, turn int, delivered []string, provenance []DeliveredAsset, abandoning ...bool) error {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	t, ok := snap.FindTask(taskID)
	if !ok {
		return ErrNotFound
	}
	_, err = s.productionGroup(&t, requestID, turn, delivered, provenance, abandoning...)
	return err
}
func (s *Service) productionGroup(t *Task, requestID string, turn int, delivered []string, provenance []DeliveredAsset, abandoning ...bool) ([]DeliveredAsset, error) {
	r, err := productionRequest(t, requestID, turn)
	if err != nil {
		return nil, err
	}
	if err := ValidateAssetProvenance(delivered, provenance); err != nil {
		return nil, err
	}
	out := make([]DeliveredAsset, 0, len(delivered))
	seen := map[string]bool{}
	for _, name := range delivered {
		if seen[name] || !slices.ContainsFunc(r.Production.Remaining(), func(a WantedAsset) bool { return a.Name == name }) {
			return nil, fmt.Errorf("%q is not a remaining wanted asset", name)
		}
		seen[name] = true
		var d DeliveredAsset
		for _, p := range provenance {
			if p.Asset == name {
				d = p
			}
		}
		matches := 0
		for _, a := range t.Attachments {
			if a.Design == requestID && a.Turn == turn && a.Asset == name {
				d.Attachment = a.ID
				d.Size = a.Size
				matches++
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("%s needs exactly one attachment from turn %d", name, turn)
		}
		path, _ := s.attachmentPath(t.ID, d.Attachment)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		d.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
		d.Size = int64(len(data))
		d.Turn = turn
		out = append(out, d)
	}
	for _, a := range t.Attachments {
		if len(delivered) == 0 && len(abandoning) > 0 && abandoning[0] {
			break
		}
		if a.Design == requestID && a.Turn == turn && a.Asset != "" && !seen[a.Asset] {
			return nil, fmt.Errorf("attachment for %s has no delivered provenance", a.Asset)
		}
	}
	return out, nil
}

// ValidateAssetProvenance checks the reply shape shared by JSON parsing and
// recording. Wanted names and attachment membership are checked at recording.
func ValidateAssetProvenance(delivered []string, provenance []DeliveredAsset) error {
	if len(delivered) > MaxAttachmentsPerSet || len(provenance) != len(delivered) {
		return errors.New("deliver at most 10 complete assets with one provenance entry each")
	}
	names := map[string]bool{}
	for _, name := range delivered {
		if strings.TrimSpace(name) == "" || names[name] {
			return errors.New("delivered assets need unique nonempty names")
		}
		names[name] = true
	}
	seen := map[string]bool{}
	for _, d := range provenance {
		if !names[d.Asset] || seen[d.Asset] || strings.TrimSpace(d.Prompt) == "" || strings.TrimSpace(d.Generator) == "" || d.Settings == nil || d.References == nil {
			return fmt.Errorf("%s needs one provenance entry: prompt, generator, settings and references", d.Asset)
		}
		seen[d.Asset] = true
		encoded, err := json.Marshal(d)
		if err != nil || len(encoded) > MaxAssetProvenanceBytes {
			return fmt.Errorf("%s provenance can be at most %s of JSON", d.Asset, ExactBytes(MaxAssetProvenanceBytes))
		}
	}
	return nil
}

func (s *Service) recordProduction(ctx context.Context, taskID, requestID string, reply DesignReply) (Task, error) {
	var written, discarded []string
	out, err := s.editTaskRecord(ctx, "", taskID, func(t *Task, v *Snapshot) error {
		r, err := productionRequest(t, requestID, reply.Turn)
		if err != nil {
			return err
		}
		if r.Production.Turns[len(r.Production.Turns)-1].Designer != reply.Designer {
			return ErrConflict
		}
		group, err := s.productionGroup(t, requestID, reply.Turn, reply.Delivered, reply.Provenance, reply.Escalate != nil)
		if err != nil {
			return err
		}
		p := r.Production
		if reply.Escalate != nil && len(group) == 0 {
			t.Attachments = slices.DeleteFunc(t.Attachments, func(a Attachment) bool {
				if a.Design == requestID && a.Turn == reply.Turn && a.Asset != "" {
					path, _ := s.attachmentPath(t.ID, a.ID)
					discarded = append(discarded, path)
					return true
				}
				return false
			})
		}
		now := s.now().UTC()
		p.Delivered = append(p.Delivered, group...)
		turn := &p.Turns[len(p.Turns)-1]
		turn.DoneAt = now
		turn.Delivered = slices.Clone(reply.Delivered)
		r.Designer = reply.Designer
		r.Input = text.Clip(strings.TrimSpace(reply.Input), 6000)
		if r.N == 0 && r.Input != "" {
			r.N = t.nextDesignNumber()
		}
		t.Failures, t.RetryAt = 0, time.Time{}
		escalate := reply.Escalate
		completed := 0
		for _, turn := range p.Turns {
			if !turn.DoneAt.IsZero() {
				completed++
			}
		}
		if escalate == nil && len(p.Remaining()) > 0 && (len(group) == 0 || completed > (len(p.Assets)+9)/10+2) {
			var remaining []string
			for _, a := range p.Remaining() {
				remaining = append(remaining, a.Name)
			}
			var delivered []string
			for _, d := range p.Delivered {
				delivered = append(delivered, d.Asset)
			}
			escalate = &DecisionInput{Title: "Production assets need your call", Context: fmt.Sprintf("Delivered: %s\nRemaining: %s", strings.Join(delivered, ", "), strings.Join(remaining, ", ")), Recommendation: "Decide how to finish the remaining assets", Choices: []string{"Use your judgment", "Stop"}}
		}
		if escalate != nil || len(p.Remaining()) == 0 {
			if p.Archive == "" {
				var archive bytes.Buffer
				if err := zip.NewWriter(&archive).Close(); err != nil {
					return err
				}
				a := Attachment{ID: uid(), Name: "rejected-variants.zip", Type: "application/zip", Size: int64(archive.Len()), Design: r.ID, By: reply.Designer, Kind: RoleDesigner, At: now, Production: true}
				path, err := s.keepFile(t.ID, a.ID, archive.Bytes())
				if err != nil {
					return err
				}
				written = append(written, path)
				t.Attachments = append(t.Attachments, a)
				p.Archive = a.ID
			}
			// keepFile avoids taking a nested store read inside this update.
			p.Provenance = uid()
			data, err := json.Marshal(p)
			if err != nil {
				return err
			}
			a, err := checkFile(NewFile{Name: "provenance.json", Made: "daemon production record", Data: data})
			if err != nil {
				return err
			}
			a.ID = p.Provenance
			path, err := s.keepFile(t.ID, a.ID, data)
			if err != nil {
				return err
			}
			written = append(written, path)
			a.Design = r.ID
			a.By = reply.Designer
			a.Kind = RoleDesigner
			a.At = now
			a.Production = true
			if err := withinLimits(t, []Attachment{a}); err != nil {
				return err
			}
			t.Attachments = append(t.Attachments, a)
		}
		if escalate != nil {
			if err := escalate.validTaskDecision(); err != nil {
				return err
			}
			r.Decision = openTaskDecision(v, t, DecisionQuestion, *escalate, now).ID
		} else if len(p.Remaining()) > 0 {
			t.Detail = fmt.Sprintf("%s handed over %d of %d assets; %d remain", reply.Designer, len(p.Delivered), len(p.Assets), len(p.Remaining()))
			recordTask(v, now, t, "task.production_group", t.Detail)
		} else {
			if err := markCurrent(v, t, r, reply.Designer, reply.Current, now); err != nil {
				return err
			}
			r.AnsweredAt = now
			t.Status, t.Detail = r.Step, "Back from "+reply.Designer+" with production assets"
			recordTask(v, now, t, "task.designed", t.Detail)
		}
		t.UpdatedAt = now
		derive(v, t)
		return nil
	})
	if err != nil {
		removeAll(written)
	} else {
		removeAll(discarded)
	}
	return out, err
}

// AttachAsset accepts one wanted asset for this turn, with the same per-file checks.
func (s *Service) AttachAsset(ctx context.Context, in DesignFiles, asset string, turn int) ([]Attachment, error) {
	var out []Attachment
	kept, paths, err := s.writeFiles(ctx, in.Task, in.Files)
	if err != nil {
		return nil, err
	}
	for i := range kept {
		kept[i].Production = true
	}
	err = s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, in.Task)
		if t == nil || t.ProjectID != in.Project {
			return ErrNotFound
		}
		r, err := productionRequest(t, in.Design, turn)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(r.Production.Remaining(), func(a WantedAsset) bool { return a.Name == asset }) {
			return fmt.Errorf("%q is not a remaining wanted asset", asset)
		}
		count := 0
		for _, a := range t.Attachments {
			if a.Design == r.ID && a.Turn == turn && a.Asset != "" {
				count++
				if a.Asset == asset {
					return fmt.Errorf("%s already has an attachment this turn", asset)
				}
			}
		}
		if count+len(kept) > MaxAttachmentsPerSet {
			return fmt.Errorf("a production turn can attach at most %d assets", MaxAttachmentsPerSet)
		}
		if len(kept) != 1 {
			return errors.New("attach exactly one file per asset")
		}
		now := s.now().UTC()
		for i := range kept {
			kept[i].Asset = asset
			kept[i].Turn = turn
			kept[i].Design = r.ID
			kept[i].By = in.By
			kept[i].Kind = in.Kind
			kept[i].At = now
		}
		if err := withinLimits(t, kept); err != nil {
			return err
		}
		t.Attachments = append(t.Attachments, kept...)
		t.UpdatedAt = now
		recordTask(v, now, t, "task.attached", fmt.Sprintf("%s attached production asset %s in turn %d", in.By, asset, turn))
		out = kept
		return nil
	})
	if err != nil {
		removeAll(paths)
	}
	return out, err
}

// AddRejected rebuilds the daemon-owned archive under a new immutable id.
// File I/O happens outside the store lock; the swap checks the original archive
// id as well as the request and turn, so concurrent additions cannot lose history.
func (s *Service) AddRejected(ctx context.Context, in DesignFiles, turn int) (Attachment, error) {
	if len(in.Files) != 1 {
		return Attachment{}, errors.New("attach one rejected variant at a time")
	}
	checked, err := checkFile(in.Files[0])
	if err != nil {
		return Attachment{}, err
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		return Attachment{}, err
	}
	t, ok := snap.FindTask(in.Task)
	if !ok || t.ProjectID != in.Project {
		return Attachment{}, ErrNotFound
	}
	r, err := productionRequest(&t, in.Design, turn)
	if err != nil {
		return Attachment{}, err
	}
	archiveID := r.Production.Archive
	var out Attachment
	var written, oldPath string
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	count := 0
	total := int64(len(in.Files[0].Data))
	if archiveID != "" {
		oldPath, _ = s.attachmentPath(in.Task, archiveID)
		z, err := zip.OpenReader(oldPath)
		if errors.Is(err, os.ErrNotExist) {
			return Attachment{}, ErrConflict
		}
		if err != nil {
			return Attachment{}, err
		}
		defer z.Close()
		for _, f := range z.File {
			total += int64(f.UncompressedSize64)
			if total > MaxRejectedBytes {
				return Attachment{}, fmt.Errorf("rejected variants can keep at most %s", ExactBytes(MaxRejectedBytes))
			}
			if err := w.Copy(f); err != nil {
				return Attachment{}, err
			}
			count++
		}
	}
	if total > MaxRejectedBytes {
		return Attachment{}, fmt.Errorf("rejected variants can keep at most %s", ExactBytes(MaxRejectedBytes))
	}
	dst, err := w.Create(fmt.Sprintf("%03d-turn-%d-%s", count+1, turn, checked.Name))
	if err != nil {
		return Attachment{}, err
	}
	if _, err = dst.Write(in.Files[0].Data); err != nil {
		return Attachment{}, err
	}
	if err = w.Close(); err != nil {
		return Attachment{}, err
	}
	if buf.Len() > MaxRejectedBytes {
		return Attachment{}, fmt.Errorf("rejected archive can be at most %s", ExactBytes(MaxRejectedBytes))
	}
	id := uid()
	written, err = s.keepFile(in.Task, id, buf.Bytes())
	if err != nil {
		return Attachment{}, err
	}
	out = Attachment{ID: id, Name: "rejected-variants.zip", Type: "application/zip", Size: int64(buf.Len()), By: in.By, Kind: in.Kind, Design: in.Design, At: s.now().UTC(), Production: true}
	err = s.store.update(ctx, func(v *Snapshot) error {
		t := task(v, in.Task)
		if t == nil || t.ProjectID != in.Project {
			return ErrNotFound
		}
		r, err := productionRequest(t, in.Design, turn)
		if err != nil {
			return err
		}
		if r.Production.Archive != archiveID {
			return ErrConflict
		}
		t.Attachments = slices.DeleteFunc(t.Attachments, func(a Attachment) bool { return a.ID == r.Production.Archive })
		t.Attachments = append(t.Attachments, out)
		r.Production.Archive = id
		t.UpdatedAt = out.At
		recordTask(v, out.At, t, "task.attached", fmt.Sprintf("%s archived rejected variant %s in turn %d", in.By, in.Files[0].Name, turn))
		return nil
	})
	if err != nil {
		removeAll([]string{written})
	} else {
		removeAll([]string{oldPath})
	}
	return out, err
}
