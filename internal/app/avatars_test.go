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
	m, err := a.CreateMember(ctx, core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "claude", Instructions: "Small commits."})
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
	if _, err := a.Core.SaveMember(ctx, m.ID, core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "codex", Avatar: &snap.Members[0].Avatar}); err != nil {
		t.Fatal(err)
	}
	after, _ := a.Snapshot(ctx)
	if after.Members[0].Avatar.Image != snap.Members[0].Avatar.Image {
		t.Fatal("saving a member lost its picture")
	}
}

func TestADrawingThatFailsSaysSoAndOneAtATimeIsDrawn(t *testing.T) {
	a := testApp(t)
	painter := &fakePainter{fail: errors.New("no image tool"), block: make(chan struct{})}
	a.Painter = painter
	ctx := context.Background()
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Rune", Kind: core.RoleReviewer, Engine: "codex"})
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
	a.Demo = true
	if err := a.DrawMember(ctx, m.ID, ""); err == nil {
		t.Fatal("demo mode drew")
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
	m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "claude"})
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
		m, _ := a.Core.SaveMember(ctx, "", core.MemberInput{Name: name, Kind: core.RoleImplementer, Engine: "claude"})
		if err := a.DrawMember(ctx, m.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	a.WaitForDrawings()
	if painter.calls.Load() != 3 || painter.most.Load() != 1 {
		t.Fatalf("%d drawings, at most %d at once", painter.calls.Load(), painter.most.Load())
	}
}
