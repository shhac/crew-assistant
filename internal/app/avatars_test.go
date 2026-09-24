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
	snap, _ := a.Core.Snapshot(ctx)
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
	snap, _ = a.Core.Snapshot(ctx)
	if snap.Members[0].Avatar.Look != "Violet bob, round glasses" || snap.Members[0].Avatar.Image == got.Avatar.Image {
		t.Fatalf("a redraw should keep the new look and picture: %+v", snap.Members[0].Avatar)
	}
	// Saving the member's details keeps the picture.
	if _, err := a.Core.SaveMember(ctx, m.ID, core.MemberInput{Name: "Ada", Kind: core.RoleImplementer, Engine: "codex", Avatar: &snap.Members[0].Avatar}); err != nil {
		t.Fatal(err)
	}
	after, _ := a.Core.Snapshot(ctx)
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
	if snap, _ := a.Core.Snapshot(ctx); !snap.Members[0].Drawing {
		t.Fatal("the member should show as being drawn")
	}
	if err := a.DrawMember(ctx, m.ID, ""); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("a second drawing of the same member: %v", err)
	}
	close(painter.block)
	a.drawings.Wait()
	snap, _ := a.Core.Snapshot(ctx)
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
	snap, _ := a.Core.Snapshot(ctx)
	if snap.Assistant.Avatar.Image != avatar.Image || snap.Assistant.Drawing {
		t.Fatalf("the dashboard should see the picture: %+v", snap.Assistant)
	}
}
