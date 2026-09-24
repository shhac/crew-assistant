package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

// squarePainter draws a different square picture each time.
type squarePainter struct{ drawn atomic.Int32 }

func (p *squarePainter) Paint(context.Context, string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 300, 300))
	img.Set(0, 0, color.RGBA{uint8(p.drawn.Add(1)), 0, 0, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes(), nil
}

func TestADrawnFaceIsServedByItsNameAndNothingElse(t *testing.T) {
	a, call := ownerApp(t)
	a.Painter = &squarePainter{}
	w := call("POST", "/api/members", `{"name":"Ada","kind":"implementer","engine":"claude"}`)
	var ada core.Member
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	a.WaitForDrawings()
	snap, _ := a.Snapshot(context.Background())
	first := snap.Members[0].Avatar.Image
	if first == "" {
		t.Fatal("the new member was not drawn")
	}
	if w := call("POST", "/api/members/"+ada.ID+"/avatar", `{"look":"Violet bob"}`); w.Code != 202 {
		t.Fatal("redraw", w.Code, w.Body.String())
	}
	a.WaitForDrawings()
	snap, _ = a.Snapshot(context.Background())
	image := snap.Members[0].Avatar.Image
	if snap.Members[0].Avatar.Look != "Violet bob" || image == first {
		t.Fatalf("the redraw should keep the look and a new picture: %+v", snap.Members[0].Avatar)
	}
	w = call("GET", "/api/avatars/"+image+"/small", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatal(w.Code, w.Header())
	}
	if cfg, err := png.DecodeConfig(w.Body); err != nil || cfg.Width != 48 {
		t.Fatalf("small picture %+v %v", cfg, err)
	}
	for _, path := range []string{"/api/avatars/" + image + "/huge", "/api/avatars/..%2F..%2Fstate.db/small", "/api/avatars/" + strings.Repeat("0", 32) + "/small"} {
		if w := call("GET", path, ""); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

func TestTheAssistantCanBeDrawnFromTheDashboard(t *testing.T) {
	a, call := ownerApp(t)
	a.Painter = &squarePainter{}
	if w := call("POST", "/api/assistant/avatar", `{"look":"Silver hair","colour":"green"}`); w.Code != 400 {
		t.Fatal("an unknown field", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/assistant/avatar", `{"look":"Silver hair"}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.WaitForDrawings()
	if avatar := a.Config().Assistant.Avatar; avatar.Image == "" || avatar.Look != "Silver hair" {
		t.Fatalf("the assistant was not drawn: %+v", avatar)
	}
}
