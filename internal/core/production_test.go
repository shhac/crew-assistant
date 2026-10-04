package core

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func productionFixture(t *testing.T, count int) (*Service, Task) {
	t.Helper()
	s, _ := fixture(t)
	p := newProject(t, s)
	pb := *p.Playbook
	pb.Roles = append(pb.Roles, Role{Name: "Dee", Kinds: []string{RoleDesigner}, Engine: "codex"})
	if _, err := s.SetPlaybook(testContext, p.ID, pb); err != nil {
		t.Fatal(err)
	}
	task, err := s.QueueTask(testContext, p.ID, TaskInput{Objective: "Frames"})
	if err != nil {
		t.Fatal(err)
	}
	s.NextTask(testContext)
	assets := make([]WantedAsset, count)
	for i := range assets {
		assets[i] = WantedAsset{Name: fmt.Sprintf("frame-%02d", i+1), Want: "An illustrated frame"}
	}
	task, err = s.AskDesign(testContext, task.ID, DesignAsk{From: "Writer", Question: "Make frames", Assets: assets, Owner: DecisionInput{Title: "Frames?", Context: "Frames are needed", Recommendation: "Finish them", Choices: []string{"Continue", "Stop"}}})
	if err != nil {
		t.Fatal(err)
	}
	if task.DesignsAt(TaskWriting) != 1 || len(task.OpenDesign().Production.Remaining()) != count {
		t.Fatal(task.Design)
	}
	return s, task
}
func startProduction(t *testing.T, s *Service, task Task) Task {
	t.Helper()
	out, err := s.StartProductionTurn(testContext, task.ID, task.OpenDesign().ID, "Dee")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func productionFile(task Task) DesignFiles {
	return DesignFiles{Project: task.ProjectID, Task: task.ID, Design: task.OpenDesign().ID, By: "Dee", Kind: RoleDesigner, While: TaskDesigning, Files: []NewFile{{Name: "frame.txt", Data: []byte("finished frame")}}}
}
func productionReply(names ...string) DesignReply {
	reply := DesignReply{Designer: "Dee", Input: "Use these frames.", Delivered: names}
	for _, name := range names {
		reply.Provenance = append(reply.Provenance, DeliveredAsset{Asset: name, Prompt: "Paint a frame", Generator: "synthetic generator", Settings: map[string]any{"size": "4x3"}, References: []string{"reference SHA-256 abc"}, SHA256: "invented"})
	}
	return reply
}
func deliverGroup(t *testing.T, s *Service, task Task, count int) Task {
	t.Helper()
	p := task.OpenDesign().Production
	turn := p.Turns[len(p.Turns)-1].N
	var names []string
	for _, a := range p.Remaining()[:count] {
		if _, err := s.AttachAsset(testContext, productionFile(task), a.Name, turn); err != nil {
			t.Fatal(err)
		}
		names = append(names, a.Name)
	}
	reply := productionReply(names...)
	reply.Turn = turn
	out, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProductionGroupsKeepTheDesignerUntilTheFinalProvenanceIsReady(t *testing.T) {
	s, task := productionFixture(t, 29)
	task = startProduction(t, s, task)
	in := productionFile(task)
	in.Files[0].Name = "rejected.txt"
	in.Files[0].Data = []byte("bad frame")
	first, err := s.AddRejected(testContext, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	oldPath, _ := s.attachmentPath(task.ID, first.ID)
	second, err := s.AddRejected(testContext, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("archive id reused")
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("old archive remains", err)
	}
	_, path, err := s.OpenAttachment(testContext, task.ProjectID, task.ID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 2 {
		t.Fatal("missing rejected variants")
	}
	for _, f := range archive.File {
		r, _ := f.Open()
		data, _ := io.ReadAll(r)
		r.Close()
		if string(data) != "bad frame" {
			t.Fatal(string(data))
		}
	}
	task = deliverGroup(t, s, task, 10)
	p := task.OpenDesign().Production
	if task.Status != TaskDesigning || len(p.Remaining()) != 19 || p.Turns[0].DoneAt.IsZero() || len(p.Turns[0].Delivered) != 10 {
		t.Fatal(task)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("finished frame")))
	if p.Delivered[0].SHA256 != expected || p.Delivered[0].Turn != 1 {
		t.Fatal(p.Delivered)
	}
	task = deliverGroup(t, s, startProduction(t, s, task), 10)
	task = deliverGroup(t, s, startProduction(t, s, task), 9)
	p = task.Design[0].Production
	if task.Status != TaskWriting || task.Design[0].Open() || len(task.Attachments) != 31 || len(p.Delivered) != 29 || len(p.Remaining()) != 0 {
		t.Fatal(task)
	}
	_, path, err = s.OpenAttachment(testContext, task.ProjectID, task.ID, p.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var record Production
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Delivered) != 29 || record.Delivered[28].Turn != 3 || record.Delivered[0].SHA256 != expected || record.Delivered[0].Prompt != "Paint a frame" || record.Delivered[0].Generator != "synthetic generator" || len(record.Delivered[0].References) != 1 || record.Delivered[0].Settings["size"] != "4x3" {
		t.Fatal(string(data))
	}
	if _, err := s.StartProductionTurn(testContext, task.ID, task.Design[0].ID, "Dee"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestProductionRejectsAnIncompleteGroupWhole(t *testing.T) {
	s, task := productionFixture(t, 2)
	task = startProduction(t, s, task)
	if _, err := s.AttachAsset(testContext, productionFile(task), "frame-01", 1); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []DesignReply{productionReply("frame-01", "frame-02"), productionReply("unwanted"), productionReply("frame-01", "frame-01"), {Designer: "Dee", Input: "No provenance", Delivered: []string{"frame-01"}}} {
		reply.Turn = 1
		if _, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply); err == nil {
			t.Fatal("bad group accepted", reply)
		}
		r := lastDesign(t, s, task.ID)
		if len(r.Production.Delivered) != 0 || !r.Production.Turns[0].DoneAt.IsZero() {
			t.Fatal(r)
		}
	}
	if _, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, productionReply("frame-01")); err == nil {
		t.Fatal("bypassed turn")
	}
	task = deliverExisting(t, s, task, "frame-01")
	if len(task.OpenDesign().Production.Remaining()) != 1 {
		t.Fatal(task)
	}
}
func deliverExisting(t *testing.T, s *Service, task Task, name string) Task {
	t.Helper()
	reply := productionReply(name)
	reply.Turn = task.OpenDesign().Production.Turns[len(task.OpenDesign().Production.Turns)-1].N
	out, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProductionRestartDropsUnfinishedAssetsAndRejectsStaleCalls(t *testing.T) {
	s, task := productionFixture(t, 3)
	task = deliverGroup(t, s, startProduction(t, s, task), 1)
	completeID := task.Attachments[0].ID
	task = startProduction(t, s, task)
	in := productionFile(task)
	attached, err := s.AttachAsset(testContext, in, "frame-02", 2)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.attachmentPath(task.ID, attached[0].ID)
	archive, err := s.AddRejected(testContext, in, 2)
	if err != nil {
		t.Fatal(err)
	}
	task = startProduction(t, s, task)
	if len(task.Attachments) != 2 || task.Attachments[0].ID != completeID || task.OpenDesign().Production.Archive != archive.ID || len(task.OpenDesign().Production.Delivered) != 1 {
		t.Fatal(task)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unfinished file retained", err)
	}
	if _, err := s.AttachAsset(testContext, in, "frame-02", 2); !errors.Is(err, ErrConflict) {
		t.Fatal("stale attachment", err)
	}
	if _, err := s.AddRejected(testContext, in, 2); !errors.Is(err, ErrConflict) {
		t.Fatal("stale rejection", err)
	}
	task = deliverGroup(t, s, task, 2)
	if task.Design[0].Production.Delivered[1].Turn != 3 {
		t.Fatal(task.Design)
	}
}

func TestProductionStoppedTaskCannotAttachOrRecord(t *testing.T) {
	s, task := productionFixture(t, 1)
	task = startProduction(t, s, task)
	in := productionFile(task)
	_, err := s.AttachAsset(testContext, in, "frame-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	s.UpdateTask(testContext, task.ID, func(t *Task, _ *Project) (string, error) { t.Status = TaskStopped; return "", nil })
	reply := productionReply("frame-01")
	reply.Turn = 1
	if _, err := s.RecordDesign(testContext, task.ID, in.Design, reply); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.AttachAsset(testContext, in, "frame-01", 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.AddRejected(testContext, in, 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	snap, _ := s.Snapshot(testContext)
	stopped, _ := snap.FindTask(task.ID)
	if stopped.Status != TaskStopped || len(stopped.Design[0].Production.Delivered) != 0 {
		t.Fatal(stopped)
	}
}

func TestProductionNoProgressAndTooManyGroupsEscalate(t *testing.T) {
	t.Run("final group beyond bound", func(t *testing.T) {
		s, task := productionFixture(t, 4)
		for range 4 {
			task = deliverGroup(t, s, startProduction(t, s, task), 1)
		}
		p := task.Design[0].Production
		if task.Status != TaskWriting || task.Design[0].Decision != "" || len(p.Remaining()) != 0 || p.Provenance == "" || p.Archive == "" {
			t.Fatal(task)
		}
	})
	for _, count := range []int{1, 12} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, task := productionFixture(t, count)
			if count == 1 {
				task = startProduction(t, s, task)
				reply := productionReply()
				reply.Turn = 1
				var err error
				task, err = s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for range 5 {
					task = deliverGroup(t, s, startProduction(t, s, task), 1)
				}
			}
			if task.Status != TaskWaiting || task.Design[0].Decision == "" {
				t.Fatal(task)
			}
			snap, _ := s.Snapshot(testContext)
			found := false
			for _, d := range snap.Decisions {
				if d.ID == task.DecisionID {
					found = strings.Contains(d.Context, "Remaining:") && strings.Contains(d.Context, "frame-")
				}
			}
			if !found {
				t.Fatal("missing remaining assets in decision")
			}
		})
	}
}

func TestProductionEscalationDiscardsUncompletedAttachmentsAndRecordsPartialProvenance(t *testing.T) {
	s, task := productionFixture(t, 3)
	task = deliverGroup(t, s, startProduction(t, s, task), 1)
	task = startProduction(t, s, task)
	in := productionFile(task)
	files, err := s.AttachAsset(testContext, in, "frame-02", 2)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.attachmentPath(task.ID, files[0].ID)
	reply := productionReply()
	reply.Turn = 2
	reply.Escalate = &DecisionInput{Title: "Cannot finish", Context: "Generator failed", Recommendation: "Use the first frame", Choices: []string{"Continue", "Stop"}}
	if err := s.ValidateProduction(testContext, task.ID, in.Design, 2, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	task, err = s.RecordDesign(testContext, task.ID, in.Design, reply)
	if err != nil {
		t.Fatal(err)
	}
	p := task.Design[0].Production
	if task.Status != TaskWaiting || len(p.Delivered) != 1 || len(p.Remaining()) != 2 || p.Provenance == "" || p.Archive == "" {
		t.Fatal(task)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("uncompleted file retained", err)
	}
	path, _ = s.attachmentPath(task.ID, p.Provenance)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record Production
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Delivered) != 1 || len(record.Remaining()) != 2 {
		t.Fatal(record)
	}

}

func TestProductionFinalGroupWithOwnerQuestionStillWritesFinalRecords(t *testing.T) {
	s, task := productionFixture(t, 1)
	task = startProduction(t, s, task)
	in := productionFile(task)
	if _, err := s.AttachAsset(testContext, in, "frame-01", 1); err != nil {
		t.Fatal(err)
	}
	reply := productionReply("frame-01")
	reply.Turn = 1
	reply.Escalate = &DecisionInput{Title: "Choose usage", Context: "All artwork is ready", Recommendation: "Use this frame", Choices: []string{"Continue", "Stop"}}
	task, err := s.RecordDesign(testContext, task.ID, in.Design, reply)
	if err != nil {
		t.Fatal(err)
	}
	p := task.Design[0].Production
	if task.Status != TaskWaiting || len(p.Remaining()) != 0 || p.Provenance == "" || p.Archive == "" {
		t.Fatal(task)
	}
}

func TestProductionLimitsDoNotChangeOrdinaryAttachmentLimits(t *testing.T) {
	task := Task{}
	for range 100 {
		task.Attachments = append(task.Attachments, Attachment{Production: true, Asset: "frame", Size: 1})
	}
	if err := withinLimits(&task, []Attachment{{Production: true, Name: "provenance.json", Size: 10}, {Production: true, Type: "application/zip", Size: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := withinLimits(&task, []Attachment{{Production: true, Asset: "extra", Size: 1}}); err == nil {
		t.Fatal("asset count limit bypassed")
	}
	task.Attachments = []Attachment{{Production: true, Asset: "large", Size: MaxProductionBytes}}
	if err := withinLimits(&task, []Attachment{{Production: true, Asset: "extra", Size: 1}}); err == nil {
		t.Fatal("asset bytes limit bypassed")
	}
	for range MaxTaskAttachments {
		task.Attachments = append(task.Attachments, Attachment{Size: 1})
	}
	if err := withinLimits(&task, []Attachment{{Size: 1}}); err == nil {
		t.Fatal("ordinary count limit changed")
	}
	if _, err := checkFile(NewFile{Name: "owner.zip", Data: []byte("zip")}); err == nil {
		t.Fatal("owner zip accepted")
	}
}

func TestOrdinaryLimitsReportOnlyOrdinaryAttachments(t *testing.T) {
	task := Task{}
	for range 29 {
		task.Attachments = append(task.Attachments, Attachment{Production: true, Asset: "frame", Size: 1})
	}
	task.Attachments = append(task.Attachments, Attachment{Production: true, Name: "provenance.json"}, Attachment{Production: true, Type: "application/zip"})
	for range MaxTaskAttachments {
		task.Attachments = append(task.Attachments, Attachment{Size: 1})
	}
	err := withinLimits(&task, []Attachment{{Size: 1}, {Production: true, Asset: "new frame"}})
	want := fmt.Sprintf("a task can keep at most %d ordinary attachments, separately from production files, and this one has %d", MaxTaskAttachments, MaxTaskAttachments)
	if err == nil || err.Error() != want {
		t.Fatal(err)
	}
	task.Attachments = task.Attachments[:31]
	task.Attachments = append(task.Attachments, Attachment{Size: MaxTaskAttachmentBytes})
	err = withinLimits(&task, []Attachment{{Size: 1}})
	if err == nil || !strings.Contains(err.Error(), "of ordinary attachments, separately from production files") {
		t.Fatal(err)
	}
}

func TestProductionAttachmentAndProvenanceLimitsRefuseWholeGroups(t *testing.T) {
	s, task := productionFixture(t, 12)
	task = startProduction(t, s, task)
	in := productionFile(task)
	for i := 1; i <= 10; i++ {
		if _, err := s.AttachAsset(testContext, in, fmt.Sprintf("frame-%02d", i), 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AttachAsset(testContext, in, "frame-11", 1); err == nil || !strings.Contains(err.Error(), "at most 10 assets") {
		t.Fatal(err)
	}
	if len(keptFiles(t, s, task.ID)) != 10 {
		t.Fatal("refused asset file was kept")
	}
	reply := productionReply("frame-01")
	reply.Turn = 1
	reply.Provenance[0].Prompt = strings.Repeat("x", MaxAssetProvenanceBytes)
	if _, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply); err == nil || !strings.Contains(err.Error(), ExactBytes(MaxAssetProvenanceBytes)) {
		t.Fatal(err)
	}
}

func TestProductionFinalRecordFailureRemovesOnlyItsNewFiles(t *testing.T) {
	s, task := productionFixture(t, 1)
	task = startProduction(t, s, task)
	if _, err := s.AttachAsset(testContext, productionFile(task), "frame-01", 1); err != nil {
		t.Fatal(err)
	}
	reply := productionReply("frame-01")
	reply.Turn = 1
	reply.Current = 99
	if _, err := s.RecordDesign(testContext, task.ID, task.OpenDesign().ID, reply); err == nil {
		t.Fatal("invalid current design accepted")
	}
	if len(keptFiles(t, s, task.ID)) != 1 {
		t.Fatal("files from failed final record retained")
	}
	r := lastDesign(t, s, task.ID)
	if len(r.Production.Delivered) != 0 || r.Production.Provenance != "" || r.Production.Archive != "" || !r.Production.Turns[0].DoneAt.IsZero() {
		t.Fatal(r)
	}
	reply.Current = CurrentThis
	back, err := s.RecordDesign(testContext, task.ID, r.ID, reply)
	if err != nil {
		t.Fatal(err)
	}
	if back.CurrentDesign != r.ID || len(back.Attachments) != 3 {
		t.Fatal(back)
	}
}

func TestProductionArchiveLimitPreservesThePreviousArchive(t *testing.T) {
	s, task := productionFixture(t, 1)
	task = startProduction(t, s, task)
	// A synthetic archive header reaches the total uncompressed limit.
	// No large allocation or live artwork is needed to test the refusal.
	path := filepath.Join(s.AttachmentsDirectory(task.ID), uid())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	if _, err = w.CreateRaw(&zip.FileHeader{Name: "prior.txt", Method: zip.Store, UncompressedSize64: MaxRejectedBytes}); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	archiveID := filepath.Base(path)
	_, err = s.UpdateTask(testContext, task.ID, func(task *Task, _ *Project) (string, error) {
		task.OpenDesign().Production.Archive = archiveID
		task.Attachments = append(task.Attachments, Attachment{ID: archiveID, Name: "rejected-variants.zip", Type: "application/zip", Design: task.OpenDesign().ID, Production: true})
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRejected(testContext, productionFile(task), 1); err == nil || !strings.Contains(err.Error(), ExactBytes(MaxRejectedBytes)) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("previous archive lost", err)
	}
	if lastDesign(t, s, task.ID).Production.Archive != archiveID || len(keptFiles(t, s, task.ID)) != 1 {
		t.Fatal("archive changed on refusal")
	}
}

func TestConcurrentRejectedArchiveSwapsKeepEverySuccessfulVariant(t *testing.T) {
	s, task := productionFixture(t, 1)
	task = startProduction(t, s, task)
	var wg sync.WaitGroup
	results := make(chan error, 16)
	start := make(chan struct{})
	for i := range 16 {
		wg.Go(func() {
			<-start
			in := productionFile(task)
			in.Files[0].Name = fmt.Sprintf("variant-%d.txt", i)
			_, err := s.AddRejected(testContext, in, 1)
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	snap, _ := s.Snapshot(testContext)
	task, _ = snap.FindTask(task.ID)
	path, _ := s.attachmentPath(task.ID, task.OpenDesign().Production.Archive)
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if successes == 0 || len(archive.File) != successes {
		t.Fatalf("%d successful writes, %d archived variants", successes, len(archive.File))
	}
}
