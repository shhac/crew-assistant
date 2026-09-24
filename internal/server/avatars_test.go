package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
)

type squarePainter struct{}

func (squarePainter) Paint(context.Context, string) ([]byte, error) {
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 300, 300)))
	return b.Bytes(), nil
}

func TestADrawnFaceIsServedByItsNameAndNothingElse(t *testing.T) {
	a, call := ownerApp(t)
	a.Painter = squarePainter{}
	w := call("POST", "/api/members", `{"name":"Ada","kind":"implementer","engine":"claude"}`)
	var ada core.Member
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ada) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("POST", "/api/members/"+ada.ID+"/avatar", `{"look":"Violet bob"}`); w.Code != 202 && w.Code != 409 {
		t.Fatal("redraw", w.Code, w.Body.String())
	}
	a.WaitForDrawings()
	snap, _ := a.Core.Snapshot(context.Background())
	image := snap.Members[0].Avatar.Image
	if image == "" {
		t.Fatal("the member was not drawn")
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
