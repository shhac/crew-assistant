package work

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

func TestSpriteFailedThenSuccessfulOriginalsSurviveHandoffAndRestart(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{productionBlock(1)}, designs: []string{productionJSON(1, 1)}}
	var failed, successful []byte
	runner.onDesigner = func(spec roles.Spec) error {
		assertBundledSkill(t, spec, core.RoleDesigner)
		spec.Opened(session.Ref{Engine: harness.Codex, ID: firstThread})
		defer spec.Ended(true)
		for i, name := range []string{"failed-strip.png", "successful-strip.png"} {
			dir := generatePNG(t, spec.RuntimeHome, firstThread, name)
			pic := image.NewRGBA(image.Rect(0, 0, 4, 4))
			pic.SetRGBA(0, 0, color.RGBA{R: uint8(i + 1), A: 255})
			var original bytes.Buffer
			if err := png.Encode(&original, pic); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), original.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			args := generatedArgs(name, name)
			if i == 0 {
				failed = bytes.Clone(original.Bytes())
				args["rejected"] = "true"
			} else {
				successful = bytes.Clone(original.Bytes())
				args["asset"] = "frame-01"
			}
			if result := callProductionTool(t, spec, args); result.IsError {
				t.Fatal(result.Content)
			}
		}
		return nil
	}
	var reopen func(*Loop) *Loop
	lp, p, task := loopApp(t, runner, "", &reopen)
	seatDesignerOn(t, lp, p.ID, "codex")
	task = stepUntil(t, lp, task.ID, func(task core.Task) bool {
		return len(task.Design) > 0 && task.Design[0].Production != nil && len(task.Design[0].Production.Delivered) == 1
	})
	verify := func(task core.Task) {
		t.Helper()
		prod := task.Design[0].Production
		if task.Design[0].Open() || len(prod.Delivered) != 1 || len(prod.Turns) != 1 || prod.Provenance == "" {
			t.Fatal("production did not complete", prod)
		}
		root := lp.Core.AttachmentsDirectory(task.ID)
		data, err := os.ReadFile(filepath.Join(root, prod.Delivered[0].Attachment))
		if err != nil || !bytes.Equal(data, successful) {
			t.Fatal("successful original lost", err)
		}
		z, err := zip.OpenReader(filepath.Join(root, prod.Archive))
		if err != nil {
			t.Fatal(err)
		}
		defer z.Close()
		if len(z.File) != 1 || !strings.HasSuffix(z.File[0].Name, "failed-strip.png") {
			t.Fatal("failed original missing", z.File)
		}
		r, err := z.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		data, err = io.ReadAll(r)
		if err != nil || !bytes.Equal(data, failed) {
			t.Fatal("failed original bytes changed", err)
		}
	}
	verify(task)
	lp = reopen(lp)
	task = settle(t, lp)
	verify(task)
	writers := writerTurns(runner)
	prod := task.Design[0].Production
	if len(writers) != 2 {
		t.Fatal("implementer did not receive completed handoff", writers)
	}
	for _, id := range []string{prod.Delivered[0].Attachment, prod.Archive, prod.Provenance} {
		if !strings.Contains(writers[1].Instructions, filepath.Join(lp.Core.AttachmentsDirectory(task.ID), id)) {
			t.Fatal("original or provenance unavailable to implementer", id)
		}
	}
}

func productionBlock(count int) string {
	var b strings.Builder
	b.WriteString("```production\n")
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "frame-%02d: An illustrated frame\n", i)
	}
	b.WriteString("\nMatch the existing art.\n```")
	return b.String()
}
func productionJSON(start, end int) string {
	answer := designAnswer{Input: "Build with these frames.", Delivered: []string{}, Provenance: []core.DeliveredAsset{}}
	for i := start; i <= end; i++ {
		name := fmt.Sprintf("frame-%02d", i)
		answer.Delivered = append(answer.Delivered, name)
		answer.Provenance = append(answer.Provenance, core.DeliveredAsset{Asset: name, Prompt: "Paint " + name, Generator: "synthetic Codex generator", Settings: map[string]any{"size": "4x4"}, References: []string{"reference.png SHA-256 abc"}})
	}
	b, _ := json.Marshal(answer)
	return string(b)
}
func callProductionTool(t *testing.T, spec roles.Spec, args map[string]string) session.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "attach_file", Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func attachProductionGroup(t *testing.T, spec roles.Spec, start, end int, rejected bool) {
	t.Helper()
	if spec.Write || spec.Opened == nil {
		t.Fatal("designer authority or generated-image hook changed")
	}
	spec.Opened(session.Ref{Engine: harness.Codex, ID: firstThread})
	defer spec.Ended(true)
	for i := start; i <= end; i++ {
		name := fmt.Sprintf("frame-%02d", i)
		file := name + ".png"
		generatePNG(t, spec.RuntimeHome, firstThread, file)
		args := generatedArgs(file, file)
		args["asset"] = name
		args["rejected"] = ""
		if result := callProductionTool(t, spec, args); result.IsError {
			t.Fatal(result.Content)
		}
	}
	if rejected {
		generatePNG(t, spec.RuntimeHome, firstThread, "bad.png")
		args := generatedArgs("bad.png", "bad.png")
		args["asset"] = ""
		args["rejected"] = "true"
		if result := callProductionTool(t, spec, args); result.IsError {
			t.Fatal(result.Content)
		}
	}
}

func TestProductionPromptAndWriterRequest(t *testing.T) {
	in := parseWriterReply(productionBlock(2), true)
	if in.productionError != nil || len(in.assets) != 2 || in.assets[0].Name != "frame-01" || !strings.Contains(in.question, "Match the existing art") || in.reply != "" {
		t.Fatal(in)
	}
	for _, block := range []string{"```production\n\n```", productionBlock(101), "```production\nframe: one\nframe: two\n```", "```production\nmissing description\n```"} {
		if parseWriterReply(block, true).productionError == nil {
			t.Fatal("bad production accepted", block)
		}
	}
	if in := parseWriterReply(productionBlock(2), false); in.assets != nil || in.question != "" {
		t.Fatal("production offered without a designer")
	}
	p := core.Production{Assets: []core.WantedAsset{{Name: "one", Want: "a robin"}, {Name: "two", Want: "a tree"}}, Delivered: []core.DeliveredAsset{{Asset: "one", Turn: 1, Prompt: "Paint a robin"}}}
	r := core.DesignRequest{From: "Writer", Step: core.TaskWriting, Question: "Make artwork", Production: &p}
	prompt := designerPrompt(core.Project{}, core.Task{}, r, true)
	for _, want := range []string{"Generating images and attaching them with attach_file (generated) is expected", "writes nothing to the repository", "'Don't deliver' means don't land, commit or approve", "at most 10", "one: a robin", "two: a tree", "Remaining assets:", "provenance", "rejected: true", "64 KiB", "use path", `"escalate":{"evidence"`, "empty delivered and provenance arrays together with a non-null escalation object", "Empty arrays with escalate:null do not abandon attached assets"} {
		if !strings.Contains(prompt, want) {
			t.Error("missing", want)
		}
	}
	if strings.Contains(prompt, "Give design input only") {
		t.Fatal(prompt)
	}
	if prompt := designerPrompt(core.Project{}, core.Task{}, r, false); !strings.Contains(prompt, "where your tools allow it") || !strings.Contains(prompt, "otherwise attach SVG or escalate") {
		t.Fatal(prompt)
	}
	r.Production = nil
	prompt = designerPrompt(core.Project{}, core.Task{}, r, true)
	if !strings.Contains(prompt, "Give design input only") || !strings.Contains(prompt, "Generating and attaching images is allowed") {
		t.Fatal(prompt)
	}
}

func TestProductionAssetsReachTheResumedImplementerAcrossARestart(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{productionBlock(12)}, designs: []string{productionJSON(1, 10), productionJSON(11, 12)}}
	group := 0
	runner.onDesigner = func(spec roles.Spec) error {
		group++
		assertBundledSkill(t, spec, core.RoleDesigner)
		if group == 1 {
			attachProductionGroup(t, spec, 1, 10, true)
		} else if group == 2 {
			if !strings.Contains(spec.Prompt, "Remaining assets:\n  - frame-11") || !strings.Contains(spec.Prompt, "designer turn 1") {
				t.Fatal(spec.Prompt)
			}
			attachProductionGroup(t, spec, 11, 12, false)
		} else {
			t.Fatal("completed group rerun")
		}
		return nil
	}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	held := stepUntil(t, a, task.ID, func(task core.Task) bool {
		return len(task.Design) > 0 && task.Design[0].Production != nil && len(task.Design[0].Production.Delivered) == 10
	})
	prod := held.OpenDesign().Production
	if held.Status != core.TaskDesigning || len(prod.Remaining()) != 2 || prod.Remaining()[0].Name != "frame-11" || len(writerTurns(runner)) != 1 {
		t.Fatal(held)
	}
	restart := New(a.Core, a.Config, false)
	restart.runner, restart.meter = runner, a.meter
	task = settle(t, restart)
	prod = task.Design[0].Production
	if group != 2 || len(prod.Turns) != 2 || len(prod.Delivered) != 12 || prod.Provenance == "" || prod.Archive == "" || task.Design[0].Open() {
		t.Fatal(task.Design)
	}
	writers := writerTurns(runner)
	for _, spec := range writers {
		assertBundledSkill(t, spec, core.RoleImplementer)
	}
	if len(writers) != 2 || string(writers[1].Resume) != `{"engine":"claude","id":"writer"}` {
		t.Fatal("implementer thread lost", writers)
	}
	for _, d := range prod.Delivered {
		path := filepath.Join(a.Core.AttachmentsDirectory(task.ID), d.Attachment)
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{path, d.SHA256, d.Prompt, d.Generator, "settings: {\"size\":\"4x4\"}", "reference.png SHA-256 abc", fmt.Sprintf("designer turn %d", d.Turn)} {
			if !strings.Contains(writers[1].Instructions, want) {
				t.Error("implementer missing", want)
			}
		}
	}
	for _, id := range []string{prod.Provenance, prod.Archive} {
		if !strings.Contains(writers[1].Instructions, filepath.Join(a.Core.AttachmentsDirectory(task.ID), id)) {
			t.Fatal("handover record missing")
		}
	}
	settle(t, restart)
	if group != 2 {
		t.Fatal("final designer turn rerun")
	}
}

func TestProductionEmptyTurnEscalatesWithRemainingAssets(t *testing.T) {
	runner := &scriptedRunner{writerReplies: []string{productionBlock(2)}, designs: []string{productionJSON(1, 0)}}
	a, p, _ := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task := settle(t, a)
	d := openDecision(t, a, task)
	if task.Status != core.TaskWaiting || len(task.Design[0].Production.Delivered) != 0 || !strings.Contains(d.Context, "Remaining: frame-01, frame-02") || len(writerTurns(runner)) != 1 {
		t.Fatal(task, d)
	}
}

func TestProductionBadWriterRequestIsCorrectedBeforeAnyDraftIsRecorded(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{"```production\n\n```", productionBlock(1)}, designs: []string{productionJSON(1, 1)}}
	runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, false); return nil }
	writes := 0
	runner.onWriter = func(dir string) {
		writes++
		path := filepath.Join(dir, "invalid-request.txt")
		if writes == 1 {
			if err := os.WriteFile(path, []byte("discard this"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("invalid turn's changes were kept", err)
		}
	}
	a, p, _ := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task := settle(t, a)
	writers := writerTurns(runner)
	if len(task.Design) != 1 || len(task.Revisions) != 1 || len(writers) != 3 || !strings.Contains(writers[1].Prompt, "Your production request could not be used") {
		t.Fatal(task, writers)
	}
}

func TestProductionStopDuringDesignerTurnDoesNotRetryOrRecord(t *testing.T) {
	runner := &scriptedRunner{writerReplies: []string{productionBlock(1)}, designs: []string{productionJSON(1, 1)}}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	calls := 0
	runner.onDesigner = func(spec roles.Spec) error {
		calls++
		attachProductionGroup(t, spec, 1, 1, false)
		if _, err := a.StopTask(context.Background(), p.ID, task.ID); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	task = settle(t, a)
	if calls != 1 || task.Status != core.TaskStopped || len(task.Design[0].Production.Delivered) != 0 || !task.Design[0].Open() {
		t.Fatal(task)
	}
}

func TestProductionBadDesignerProvenanceIsRetriedWithoutRecordingAPartialGroup(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{productionBlock(1)}, designs: []string{`{"input":"Use the frame","delivered":["frame-01"],"provenance":[]}`, ""}}
	calls := 0
	var a *Loop
	var taskID, originalID, originalPath string
	var originalSpec roles.Spec
	runner.onDesigner = func(spec roles.Spec) error {
		calls++
		assertBundledSkill(t, spec, core.RoleDesigner)
		if calls == 1 {
			originalSpec = spec
			attachProductionGroup(t, spec, 1, 1, true)
			task := taskByID(t, a, taskID)
			originalID = task.Attachments[0].ID
			originalPath = filepath.Join(a.Core.AttachmentsDirectory(taskID), originalID)
		} else {
			if !strings.Contains(spec.Prompt, "Its unfinished asset group was discarded") || len(spec.Resume) != 0 {
				t.Fatal(spec.Prompt, string(spec.Resume))
			}
			if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
				t.Fatal("old attachment survived correction", err)
			}
			if strings.Contains(spec.Instructions, originalID) || !strings.Contains(spec.Instructions, "rejected-variants.zip") {
				t.Fatal("attachment instructions not refreshed", spec.Instructions)
			}
			stale := callProductionTool(t, originalSpec, map[string]string{"name": "late.txt", "content": "late", "path": "", "generated": "", "asset": "frame-01", "rejected": ""})
			if !stale.IsError {
				t.Fatal("old turn still accepts attachments")
			}
			// The replacement is generated and attached in the correction session.
			// Its provenance comes from that attempt, not the discarded generation.
			attachProductionGroup(t, spec, 1, 1, false)
			answer := designAnswer{Input: "Use the replacement", Delivered: []string{"frame-01"}, Provenance: []core.DeliveredAsset{{Asset: "frame-01", Prompt: "Paint replacement from attempt 2", Generator: "synthetic correction generator", Settings: map[string]any{"attempt": 2}, References: []string{}}}}
			data, _ := json.Marshal(answer)
			runner.designs[0] = string(data)
		}
		return nil
	}
	var p core.Project
	var task core.Task
	a, p, task = loopApp(t, runner, "")
	taskID = task.ID
	seatDesignerOn(t, a, p.ID, "codex")
	task = settle(t, a)
	production := task.Design[0].Production
	if calls != 2 || len(production.Turns) != 2 || !production.Turns[0].DoneAt.IsZero() || len(production.Delivered) != 1 || len(task.Revisions) != 1 {
		t.Fatal(task)
	}
	asset := production.Delivered[0]
	if asset.Turn != 2 || asset.Attachment == originalID || asset.Prompt != "Paint replacement from attempt 2" || asset.Generator != "synthetic correction generator" {
		t.Fatal(asset)
	}
	if production.Archive == "" || production.Provenance == "" || !activityHas(t, a, "dropped 1 files from an unfinished turn") {
		t.Fatal("missing cleanup or production records", task)
	}
}

func TestParseDesignRejectsIncompleteProductionProvenance(t *testing.T) {
	for _, reply := range []string{
		`{"input":"Use it","delivered":["one"],"provenance":[]}`,
		`{"input":"Use it","delivered":["one"],"provenance":[{"asset":"one","prompt":"Paint","generator":"Test"}]}`,
		`{"input":"Use it","delivered":["one"],"provenance":[{"asset":"other","prompt":"Paint","generator":"Test","settings":{},"references":[]}]}`,
	} {
		if _, err := parseDesign(reply); err == nil {
			t.Fatal("invalid provenance accepted", reply)
		}
	}
}

func TestProductionCrashAttachmentsAreDroppedBeforeTheNextSession(t *testing.T) {
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{productionBlock(2)}, designs: []string{productionJSON(1, 2)}}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task = stepUntil(t, a, task.ID, withDesigner)
	started, err := a.Core.StartProductionTurn(context.Background(), task.ID, task.OpenDesign().ID, "Dee")
	if err != nil {
		t.Fatal(err)
	}
	in := core.DesignFiles{Project: p.ID, Task: task.ID, Design: started.OpenDesign().ID, By: "Dee", Kind: core.RoleDesigner, Files: []core.NewFile{{Name: "unfinished.txt", Data: []byte("unfinished")}}}
	files, err := a.Core.AttachAsset(context.Background(), in, "frame-01", 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Core.AttachmentsDirectory(task.ID), files[0].ID)
	runner.onDesigner = func(spec roles.Spec) error {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unfinished file visible in next turn", err)
		}
		attachProductionGroup(t, spec, 1, 2, false)
		return nil
	}
	restart := New(a.Core, a.Config, false)
	restart.runner, restart.meter = runner, a.meter
	task = settle(t, restart)
	if len(task.Design[0].Production.Turns) != 2 || !task.Design[0].Production.Turns[0].DoneAt.IsZero() || task.Design[0].Production.Delivered[0].Turn != 2 {
		t.Fatal(task.Design)
	}
}

func TestProductionEscalationAfterAttachingReachesOwner(t *testing.T) {
	runner := &scriptedRunner{writerReplies: []string{productionBlock(2)}, designs: []string{`{"input":"Generator failed","delivered":[],"provenance":[],"escalate":{"evidence":"Tool failed","alternatives":["Use fewer frames"],"consequences":"Less motion","recommendation":"Use fewer frames"}}`}}
	runner.onDesigner = func(spec roles.Spec) error { attachProductionGroup(t, spec, 1, 1, false); return nil }
	a, p, _ := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task := settle(t, a)
	if task.Status != core.TaskWaiting || task.Failures != 0 || len(task.Design[0].Production.Delivered) != 0 || task.Design[0].Production.Provenance == "" {
		t.Fatal(task)
	}
	if len(turns(runner, "asks for your design input")) != 1 {
		t.Fatal("escalation retried")
	}
	provenance := task.Design[0].Production.Provenance
	decision, err := a.Core.AnswerDecision(context.Background(), task.DecisionID, "Use your judgment", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.applyAnswer(context.Background(), task, decision); err != nil {
		t.Fatal(err)
	}
	task = taskByID(t, a, task.ID)
	if task.Status != core.TaskWriting || task.Design[0].AnsweredAt.IsZero() || task.Design[0].Production.Provenance != provenance {
		t.Fatal(task)
	}

}

func TestProductionAbandonmentRequiresEscalation(t *testing.T) {
	runner := &scriptedRunner{writerReplies: []string{productionBlock(2)}, designs: []string{
		`{"input":"Cannot finish","delivered":[],"provenance":[],"escalate":null}`,
		`{"input":"Cannot finish","delivered":[],"provenance":[],"escalate":{"recommendation":"Use fewer frames"}}`,
	}}
	calls := 0
	runner.onDesigner = func(spec roles.Spec) error {
		calls++
		if !strings.Contains(spec.Prompt, "empty delivered and provenance arrays together with a non-null escalation object") {
			t.Fatal("abandonment contract missing", spec.Prompt)
		}
		if calls == 1 {
			attachProductionGroup(t, spec, 1, 1, false)
		} else if !strings.Contains(spec.Prompt, "has no delivered provenance") {
			t.Fatal("empty arrays without escalation were not refused", spec.Prompt)
		}
		return nil
	}
	a, p, _ := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task := settle(t, a)
	if calls != 2 || task.Status != core.TaskWaiting || task.Failures != 0 {
		t.Fatal(calls, task)
	}
	production := task.Design[0].Production
	if len(production.Turns) != 2 || len(production.Delivered) != 0 || len(production.Remaining()) != 2 {
		t.Fatal(production)
	}
	for _, attachment := range task.Attachments {
		if attachment.Asset != "" {
			t.Fatal("abandoned asset retained", attachment)
		}
	}
}
