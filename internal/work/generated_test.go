package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/roles"
)

// generatePNG stands in for Codex's image generation tool: it saves a PNG
// where Codex saves what a session generates.
func generatePNG(t *testing.T, home, thread, name string) string {
	t.Helper()
	dir := filepath.Join(home, "generated_images", thread)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var pic bytes.Buffer
	png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	if err := os.WriteFile(filepath.Join(dir, name), pic.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func generatedArgs(name, generated string) map[string]string {
	return map[string]string{"name": name, "content": "", "path": "", "generated": generated}
}

const (
	firstThread  = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	secondThread = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c"
)

// A Codex designer generates an icon in its turn and attaches it by name;
// it is kept with the design, and the turn's generated images are gone
// afterwards. A reply that had to be asked for again runs in a new session,
// whose tools reach only that session's images.
func TestACodexDesignerAttachesAnImageItGeneratedInItsTurn(t *testing.T) {
	t.Parallel()
	var results []session.ToolResult
	var homes []string
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}, designs: []string{"not JSON", `{"input": "Use this icon.", "current": "this", "escalate": null}`}}
	runner.onDesigner = func(spec roles.Spec) error {
		if spec.Opened == nil || spec.Ended == nil {
			t.Error("a Codex designer's turn does not hear its session")
			return nil
		}
		// Each session ends confirmed gone, as a released one does.
		defer spec.Ended(true)
		thread := firstThread
		if len(homes) > 0 {
			thread = secondThread
		}
		homes = append(homes, spec.RuntimeHome)
		spec.Opened(session.Ref{Engine: harness.Codex, ID: thread})
		generatePNG(t, spec.RuntimeHome, thread, "ig_"+thread[len(thread)-1:]+".png")
		for _, name := range []string{"ig_b.png", "ig_c.png"} {
			raw, _ := json.Marshal(generatedArgs("", name))
			result, err := spec.Handler.CallTool(context.Background(), session.ToolCall{Name: "attach_file", Arguments: raw})
			if err != nil {
				t.Fatal(err)
			}
			results = append(results, result)
		}
		return nil
	}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	task = settle(t, a)
	if len(results) != 4 {
		t.Fatalf("%d attach calls", len(results))
	}
	// The first turn attaches its own image; the second can't reach the
	// first's, and attaches its own.
	if !strings.Contains(results[0].Content, "Attached ig_b.png") || !results[1].IsError || !results[2].IsError || !strings.Contains(results[3].Content, "Attached ig_c.png") {
		t.Fatalf("attach results %+v", results)
	}
	if len(task.Attachments) != 2 || task.Attachments[1].Name != "ig_c.png" || task.Attachments[1].Type != "image/png" || task.Attachments[1].Design != task.Design[0].ID {
		t.Fatalf("attachments %+v", task.Attachments)
	}
	for _, thread := range []string{firstThread, secondThread} {
		if _, err := os.Stat(filepath.Join(homes[0], "generated_images", thread)); !os.IsNotExist(err) {
			t.Errorf("the turn's generated images were left behind: %v", err)
		}
	}
	designers := turns(runner, "asks for your design input")
	if len(designers) != 2 || !strings.Contains(designers[0].Prompt, "raster image, such as an icon, an illustration or a mockup, you can make one with your image generation tool") {
		t.Fatalf("the designer was not told it can generate images: %d turns", len(designers))
	}
	if !strings.Contains(designers[0].Instructions, "an image you generated in this turn") {
		t.Fatalf("the designer's guide: %s", designers[0].Instructions)
	}
}

// Only an image in the folder of the session running now can be attached:
// a path out of it, a link, a hidden file or one in another folder is
// refused, and a turn with nothing generated gets a tool error.
func TestOnlyThisTurnsGeneratedImagesCanBeAttached(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	held := stepUntil(t, a, task.ID, withDesigner)
	designer, _ := held.Designer()
	tools := a.toolsFor(held, core.RoleDesigner, designer)
	if tools.generated == nil {
		t.Fatal("a Codex designer can't attach generated images")
	}
	home := t.TempDir()
	tools.generated.home = home
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); !got.IsError || !strings.Contains(got.Content, "can't be found") {
		t.Fatalf("before the session opened: %+v", got)
	}
	tools.generated.opened(session.Ref{Engine: harness.Codex, ID: "../../elsewhere"})
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); !got.IsError {
		t.Fatalf("an id that isn't a session's: %+v", got)
	}
	tools.generated.opened(session.Ref{Engine: harness.Codex, ID: firstThread})
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); !got.IsError || !strings.Contains(got.Content, "no images") {
		t.Fatalf("with nothing generated: %+v", got)
	}
	dir := generatePNG(t, home, firstThread, "icon.png")
	generatePNG(t, home, firstThread, ".hidden.png")
	generatePNG(t, filepath.Join(dir, "sub"), "", "nested.png")
	other := generatePNG(t, home, secondThread, "other.png")
	outside := generatePNG(t, t.TempDir(), "", "secret.png")
	if err := os.Symlink(filepath.Join(outside, "secret.png"), filepath.Join(dir, "link.png")); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]string{
		"a path out":               generatedArgs("", "../"+secondThread+"/other.png"),
		"another session's image":  generatedArgs("", filepath.Join(other, "other.png")),
		"an absolute path outside": generatedArgs("", filepath.Join(outside, "secret.png")),
		"a path in a subfolder":    generatedArgs("", "sub/generated_images/nested.png"),
		"a symlink out":            generatedArgs("", "link.png"),
		"a hidden file":            generatedArgs("", ".hidden.png"),
		"a missing image":          generatedArgs("", "nope.png"),
		"generated and content":    {"name": "a.svg", "content": "<svg/>", "path": "", "generated": "icon.png"},
		"generated and path":       {"name": "", "content": "", "path": "icon.png", "generated": "icon.png"},
		"an unsafe name":           generatedArgs("../../icon.png", "icon.png"),
	} {
		if got := callTool(t, tools, "attach_file", args); !got.IsError {
			t.Errorf("%s was attached: %s", name, got.Content)
		}
	}
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); got.IsError || !strings.Contains(got.Content, "Attached icon.png") {
		t.Fatalf("this turn's image: %+v", got)
	}
	// generated is a plain file name: even a full path into this turn's
	// folder is refused.
	if got := callTool(t, tools, "attach_file", generatedArgs("icon-again.png", filepath.Join(dir, "icon.png"))); !got.IsError || !strings.Contains(got.Content, "not a path") {
		t.Fatalf("this turn's image by its full path: %+v", got)
	}
	snap, _ := a.Core.Snapshot(context.Background())
	now, _ := findTask(snap, "", task.ID)
	if len(now.Attachments) != 1 || now.Attachments[0].Name != "icon.png" || now.Attachments[0].Type != "image/png" || now.Attachments[0].Design != held.Design[0].ID {
		t.Fatalf("attachments %+v", now.Attachments)
	}
	tools.generated.closed(true)
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); !got.IsError {
		t.Fatalf("attached after its session ended: %+v", got)
	}
	tools.generated.remove()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the turn's images were kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another session's images were removed: %v", err)
	}
}

// A daemon that stopped mid-turn never ran the turn's cleanup, so the images
// it generated are left. A restart that can't confirm the designer's turn
// ended keeps them, since that turn may still be making them; one that
// confirms it removes them, and the designer then runs again as usual.
func TestARestartRemovesImagesACrashedDesignerTurnLeftOnceItEnded(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	stepUntil(t, a, task.ID, withDesigner)
	ctx := context.Background()
	// The old daemon claimed the designer's turn, launched it, and stopped
	// while it was generating.
	claimed, err := a.Core.Schedule(ctx, func(core.Role) string { return "" })
	if err != nil || len(claimed) != 1 || claimed[0].Task.ID != task.ID {
		t.Fatalf("claimed %+v %v", claimed, err)
	}
	launch := a.launchDir(claimed[0].Claim.Token)
	if err := os.MkdirAll(launch, 0o700); err != nil {
		t.Fatal(err)
	}
	left := generatePNG(t, a.runtimeHome("codex"), firstThread, "ig_b.png")
	restart := func(confirmed bool) *Loop {
		again := New(a.Core, a.Config, false)
		again.runner, again.meter = runner, a.meter
		reclaimed := false
		again.reclaim = func(_ context.Context, dir string) (session.Reclamation, error) {
			// The turn is ended before anything it left is removed.
			if _, err := os.Stat(left); err != nil {
				t.Errorf("the images went before the turn was reclaimed: %v", err)
			}
			reclaimed = reclaimed || dir == launch
			if !confirmed {
				return session.Reclamation{Found: true}, session.ErrUnreclaimed
			}
			return session.Reclamation{Confirmed: true}, nil
		}
		if err := again.resume(ctx); err != nil {
			t.Fatal(err)
		}
		if !reclaimed {
			t.Fatal("the stopped daemon's turn was not reclaimed")
		}
		return again
	}
	held := restart(false)
	if now := taskByID(t, held, task.ID); len(now.Claims) != 1 || now.Claims[0].Held == "" {
		t.Fatalf("the unconfirmed designer's task should stay held: %+v", now.Claims)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("a turn that may still run lost its generated images: %v", err)
	}
	again := restart(true)
	if _, err := os.Stat(filepath.Join(a.runtimeHome("codex"), "generated_images")); !os.IsNotExist(err) {
		t.Fatalf("a crashed turn's generated images were kept: %v", err)
	}
	task = settle(t, again)
	if len(task.Design) != 1 || task.Design[0].Input == "" || len(task.Attachments) != 0 {
		t.Fatalf("designs %+v, attachments %+v", task.Design, task.Attachments)
	}
}

// A designer's turn that fails with its session not confirmed gone, as a
// stopped one can, keeps that session's images, since it may still be
// making them; a restart that reclaims the session removes them.
func TestAnUnconfirmedDesignerTurnKeepsItsImagesUntilARestartReclaimsIt(t *testing.T) {
	t.Parallel()
	var left, launch string
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	runner.onDesigner = func(spec roles.Spec) error {
		if left != "" {
			return nil
		}
		spec.Opened(session.Ref{Engine: harness.Codex, ID: firstThread})
		left = generatePNG(t, spec.RuntimeHome, firstThread, "ig_b.png")
		// Its harness couldn't be confirmed gone, so its launch is kept for
		// a restart to reclaim.
		launch = spec.LaunchDir
		if err := os.MkdirAll(launch, 0o700); err != nil {
			t.Fatal(err)
		}
		spec.Ended(false)
		return errors.New("the turn was stopped")
	}
	a, p, task := loopApp(t, runner, "")
	seatDesignerOn(t, a, p.ID, "codex")
	stepUntil(t, a, task.ID, withDesigner)
	stepUntil(t, a, task.ID, func(core.Task) bool { return left != "" })
	if launch == "" {
		t.Fatal("the designer's turn recorded no launch")
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("a session that may still run lost its generated images: %v", err)
	}
	again := New(a.Core, a.Config, false)
	again.runner, again.meter = runner, a.meter
	again.reclaim = func(context.Context, string) (session.Reclamation, error) {
		return session.Reclamation{Confirmed: true}, nil
	}
	if err := again.resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(left)); !os.IsNotExist(err) {
		t.Fatalf("the reclaimed session's images were kept: %v", err)
	}
}

// A designer on an engine that can't generate images is told so, and
// attach_file offers it no generated images.
func TestADesignerThatCantGenerateImagesIsToldSo(t *testing.T) {
	t.Parallel()
	runner := &scriptedRunner{reviews: []string{pass}, writerReplies: []string{askDesign}}
	a, p, task := loopApp(t, runner, "")
	seatDesigner(t, a, p.ID)
	held := stepUntil(t, a, task.ID, withDesigner)
	tools := designerTools(t, a, held, t.TempDir())
	if tools.generated != nil || strings.Contains(tools.guide(), "generated") {
		t.Fatalf("a Claude designer was offered generated images: %s", tools.guide())
	}
	for _, d := range tools.Definitions() {
		if d.Name == "attach_file" && slices.Contains(d.Schema["required"].([]string), "generated") {
			t.Fatal("a Claude designer's attach_file takes generated images")
		}
	}
	if got := callTool(t, tools, "attach_file", generatedArgs("", "icon.png")); !got.IsError || !strings.Contains(got.Content, "isn't available") {
		t.Fatalf("a generated image: %+v", got)
	}
	settle(t, a)
	designers := turns(runner, "asks for your design input")
	if len(designers) != 1 || designers[0].Opened != nil || !strings.Contains(designers[0].Prompt, "Image generation isn't available to you here") || strings.Contains(designers[0].Prompt, "image generation tool") {
		t.Fatalf("a Claude designer was not told it can't generate images: %d turns", len(designers))
	}
}
