package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/avatars"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

type fakePainter struct {
	mu    sync.Mutex
	seen  []string
	fail  error
	block chan struct{}
}

func (f *fakePainter) Paint(_ context.Context, character string) ([]byte, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.seen = append(f.seen, character)
	f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	img.Set(1, 1, color.RGBA{uint8(len(f.seen)), 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes(), nil
}

func TestCodexDrawsAMemberInTheBackground(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	m, err := a.CreateMember(ctx, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Instructions: "Small commits."})
	if err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	snap, _ := a.Snapshot(ctx)
	got := snap.Members[0]
	if got.Avatar.Image == "" || got.Drawing || got.DrawError != "" {
		t.Fatalf("a new member should be drawn: %+v", got)
	}
	if !strings.Contains(painter.seen[0], "Ada, the implementer") || !strings.Contains(painter.seen[0], "Design their look yourself") {
		t.Fatalf("character %q", painter.seen[0])
	}
	if _, ok := a.Avatars().Path(got.Avatar.Image, "small"); !ok {
		t.Fatal("the picture was not stored")
	}
	if err := a.DrawMember(ctx, m.ID, "Violet bob, round glasses"); err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	snap, _ = a.Snapshot(ctx)
	if snap.Members[0].Avatar.Look != "Violet bob, round glasses" || snap.Members[0].Avatar.Image == got.Avatar.Image {
		t.Fatalf("a redraw should keep the new look and picture: %+v", snap.Members[0].Avatar)
	}
	// Saving the member's details keeps the picture.
	if _, err := a.Core.SaveMember(ctx, m.ID, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "codex", Avatar: &snap.Members[0].Avatar}); err != nil {
		t.Fatal(err)
	}
	after, _ := a.Snapshot(ctx)
	if after.Members[0].Avatar.Image != snap.Members[0].Avatar.Image {
		t.Fatal("saving a member lost its picture")
	}
}

func TestAMembersDescriptionIsDrawnWithTheirLook(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	plain, err := a.CreateMember(ctx, core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "codex", Instructions: "Read twice."})
	if err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	ofRune, ofAda := "Rune, "+roleWords([]string{core.RoleReviewer}), "Ada, "+roleWords([]string{core.RoleImplementer})
	if want := ofRune + " on a small software team. How they work: Read twice. Design their look yourself to suit them."; painter.seen[0] != want {
		t.Fatalf("an empty description changed the prompt:\n got %q\nwant %q", painter.seen[0], want)
	}
	described, err := a.CreateMember(ctx, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Description: "A tall woman in her sixties with silver hair."})
	if err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	if err := a.DrawMember(ctx, described.ID, "Violet bob, round glasses"); err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	if err := a.DrawMember(ctx, plain.ID, "Green cap"); err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	for i, want := range []string{
		ofAda + " on a small software team. Who they are: A tall woman in her sixties with silver hair. Design their look yourself to suit them.",
		ofAda + " on a small software team. Who they are: A tall woman in her sixties with silver hair. Their look: Violet bob, round glasses",
		ofRune + " on a small software team. How they work: Read twice. Their look: Green cap",
	} {
		if painter.seen[i+1] != want {
			t.Fatalf("drawing %d:\n got %q\nwant %q", i+1, painter.seen[i+1], want)
		}
	}
}

func TestADrawingThatFailsSaysSoAndOneAtATimeIsDrawn(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{fail: errors.New("no image tool"), block: make(chan struct{})}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	if err := a.DrawMember(ctx, m.ID, ""); err != nil {
		t.Fatal(err)
	}
	if snap, _ := a.Snapshot(ctx); !snap.Members[0].Drawing {
		t.Fatal("the member should show as being drawn")
	}
	if err := a.DrawMember(ctx, m.ID, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a second drawing of the same member: %v", err)
	}
	close(painter.block)
	a.drawings.Wait()
	snap, _ := a.Snapshot(ctx)
	if snap.Members[0].Drawing || !strings.Contains(snap.Members[0].DrawError, "no image tool") || snap.Members[0].Avatar.Image != "" {
		t.Fatalf("a failed drawing: %+v", snap.Members[0])
	}
}

// A painter that says it cannot draw now, as Codex does while the owner's
// usage limit holds.
type gatedPainter struct {
	fakePainter
	notNow error
}

func (g *gatedPainter) Ready(context.Context) error { return g.notNow }

func TestAPainterThatCannotDrawNowIsNotAsked(t *testing.T) {
	a := testApp(t)
	painter := &gatedPainter{notNow: errors.New("Waiting for Codex usage to reset")}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kinds: []string{core.RoleReviewer}, Engine: "codex"})
	if err := a.DrawMember(ctx, m.ID, ""); !errors.Is(err, painter.notNow) {
		t.Fatalf("the gate's reason should be returned: %v", err)
	}
	a.WaitForDrawings()
	snap, _ := a.Snapshot(ctx)
	if len(painter.seen) != 0 || snap.Members[0].Drawing {
		t.Fatalf("a held painter was asked to draw: %v %+v", painter.seen, snap.Members[0])
	}
}

func TestWithoutAPainterAMemberIsKeptAndSaysItWasNotDrawn(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	m, err := a.CreateMember(ctx, core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err != nil || m.ID == "" {
		t.Fatalf("the member should be kept: %+v %v", m, err)
	}
	snap, _ := a.Snapshot(ctx)
	if got := snap.Members[0]; got.Drawing || !strings.Contains(got.DrawError, "Couldn't draw it: drawing needs Codex") {
		t.Fatalf("a drawing that cannot start: %+v", got)
	}
}

func TestApplyingAnIdentityDrawsTheAssistant(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	if err := a.DrawAssistant(ctx, "Silver hair, a green scarf"); err != nil {
		t.Fatal(err)
	}
	a.drawings.Wait()
	avatar := a.Config().Assistant.Avatar
	if avatar.Image == "" || avatar.Look != "Silver hair, a green scarf" || !strings.Contains(painter.seen[0], "a green scarf") {
		t.Fatalf("assistant avatar %+v, character %q", avatar, painter.seen)
	}
	snap, _ := a.Snapshot(ctx)
	if snap.Assistant.Avatar.Image != avatar.Image || snap.Assistant.Drawing {
		t.Fatalf("the dashboard should see the picture: %+v", snap.Assistant)
	}
}

func TestTheLongestLookReachesThePainterWhole(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude", Instructions: strings.Repeat("Small commits. ", 20)})
	look := "Violet" + strings.Repeat(" violet", (config.MaxLook-10)/7) + " bob"
	look += strings.Repeat("!", config.MaxLook-len(look))
	if err := a.DrawMember(ctx, m.ID, look); err != nil {
		t.Fatal(err)
	}
	a.WaitForDrawings()
	if !strings.Contains(avatars.Prompt(painter.seen[0]), look+"\n\"\"\"") {
		t.Fatal("the look was cut short")
	}
	if err := a.DrawMember(ctx, m.ID, look+"x"); err == nil {
		t.Fatal("a look over the bound was drawn")
	}
}

// countingPainter records how many drawings ran and the most at once.
type countingPainter struct {
	release  chan struct{}
	calls    atomic.Int32
	inFlight atomic.Int32
	most     atomic.Int32
}

func (p *countingPainter) Paint(ctx context.Context, character string) ([]byte, error) {
	p.calls.Add(1)
	now := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	for {
		most := p.most.Load()
		if now <= most || p.most.CompareAndSwap(most, now) {
			break
		}
	}
	if p.release != nil {
		<-p.release
	}
	return (&fakePainter{}).Paint(ctx, character)
}

func TestOnlyOneOfManyRequestsToDrawAMemberStartsIt(t *testing.T) {
	a := testApp(t)
	painter := &countingPainter{release: make(chan struct{})}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	const n = 16
	start := make(chan struct{})
	errs := make(chan error, n)
	var ready sync.WaitGroup
	for i := 0; i < n; i++ {
		ready.Add(1)
		go func() {
			ready.Done()
			<-start
			errs <- a.DrawMember(ctx, m.ID, "")
		}()
	}
	ready.Wait()
	close(start)
	started := 0
	for i := 0; i < n; i++ {
		err := <-errs
		switch {
		case err == nil:
			started++
		case !errors.Is(err, core.ErrConflict):
			t.Fatalf("a refused drawing should be a conflict: %v", err)
		}
	}
	close(painter.release)
	a.WaitForDrawings()
	if started != 1 || painter.calls.Load() != 1 {
		t.Fatalf("%d drawings started and %d painted, want 1", started, painter.calls.Load())
	}
}

func TestDrawingsOfDifferentMembersNeverOverlap(t *testing.T) {
	a := testApp(t)
	painter := &countingPainter{}
	a.Painter = painter
	ctx := context.Background()
	for _, name := range []string{"Ada", "Rune", "Zed"} {
		m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: name, Kinds: []string{core.RoleImplementer}, Engine: "claude"})
		if err := a.DrawMember(ctx, m.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	a.WaitForDrawings()
	if painter.calls.Load() != 3 || painter.most.Load() != 1 {
		t.Fatalf("%d drawings, at most %d at once", painter.calls.Load(), painter.most.Load())
	}
}

// waitingPainter draws nothing until its drawing is called off.
type waitingPainter struct{ started chan struct{} }

func (p waitingPainter) Paint(ctx context.Context, _ string) ([]byte, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func waitForDrawings(t *testing.T, a *App) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		a.WaitForDrawings()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a drawing outlived the daemon")
	}
}

func TestStoppingTheDaemonStopsADrawing(t *testing.T) {
	a := testApp(t)
	a.Painter = waitingPainter{started: make(chan struct{})}
	life, stop := context.WithCancel(context.Background())
	a.setLife(life)
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err := a.DrawMember(ctx, m.ID, "Violet bob"); err != nil {
		t.Fatal(err)
	}
	<-a.Painter.(waitingPainter).started
	stop()
	waitForDrawings(t, a)
	snap, _ := a.Snapshot(ctx)
	if got := snap.Members[0]; got.Avatar.Image != "" || got.Avatar.Look != "" || got.Drawing {
		t.Fatalf("a drawing called off should change nothing: %+v", got)
	}
}

// A drawing's status is not stored, so a daemon that restarts forgets one it
// could not finish rather than showing it as under way for good.
func TestARestartedAppDoesNotShowADrawingUnderWay(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{block: make(chan struct{})}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err := a.DrawMember(ctx, m.ID, ""); err != nil {
		t.Fatal(err)
	}
	restarted := New(a.Core, a.Config(), a.configPath, false)
	restarted.Painter = &fakePainter{}
	if snap, _ := restarted.Snapshot(ctx); snap.Members[0].Drawing {
		t.Fatal("a restarted app shows a drawing it is not doing")
	}
	if err := restarted.DrawMember(ctx, m.ID, ""); err != nil {
		t.Fatalf("a redraw after a restart: %v", err)
	}
	restarted.WaitForDrawings()
	close(painter.block)
	a.WaitForDrawings()
	if snap, _ := restarted.Snapshot(ctx); snap.Members[0].Avatar.Image == "" {
		t.Fatal("the redraw was not kept")
	}
}

func TestDeletingAMemberWhileItIsDrawnLeavesNothingBusy(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{block: make(chan struct{})}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kinds: []string{core.RoleImplementer}, Engine: "claude"})
	if err := a.DrawMember(ctx, m.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Core.DeleteMember(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	close(painter.block)
	a.WaitForDrawings()
	if a.drawingOf(m.ID).busy {
		t.Fatal("a deleted member is stuck being drawn")
	}
	if snap, _ := a.Snapshot(ctx); len(snap.Members) != 0 {
		t.Fatalf("the drawing brought a deleted member back: %+v", snap.Members)
	}
}
