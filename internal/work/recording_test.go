package work

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/session"
)

func pngFrame(t *testing.T, w, h int, fill func(x, y int) color.Color) session.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, fill(x, y))
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return session.Image{MediaType: "image/png", Data: b.Bytes()}
}

func solid(c color.Color) func(int, int) color.Color { return func(int, int) color.Color { return c } }

// QA's screenshots become one recording of the turn, scaled down, in the
// order taken, holding the last frame longest.
func TestARecordingPlaysQAsScreenshotsInOrder(t *testing.T) {
	t.Parallel()
	frames := []session.Image{
		pngFrame(t, 1600, 1000, solid(color.RGBA{255, 0, 0, 255})),
		{MediaType: "image/png", Data: []byte("not an image")},
		pngFrame(t, 1600, 1000, solid(color.RGBA{0, 0, 255, 255})),
		pngFrame(t, 400, 300, solid(color.RGBA{0, 255, 0, 255})),
	}
	b, ok := recording(frames)
	if !ok {
		t.Fatal("no recording")
	}
	anim, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(anim.Image) != 3 || anim.Delay[0] != 100 || anim.Delay[2] != 300 {
		t.Fatalf("frames %d, delays %v", len(anim.Image), anim.Delay)
	}
	if w := anim.Image[0].Bounds().Dx(); w != recordingWidths[0] {
		t.Fatalf("a wide screenshot was not scaled down: %d", w)
	}
	if w := anim.Image[2].Bounds().Dx(); w != 400 {
		t.Fatalf("a narrow screenshot was scaled: %d", w)
	}
	r, g, bl, _ := anim.Image[1].At(10, 10).RGBA()
	if bl < r || bl < g {
		t.Fatal("frames are out of order")
	}
	if _, ok := recording(frames[:2]); ok {
		t.Fatal("a single readable screenshot made a recording")
	}
}

// Screenshots that compress badly still give a recording within one
// attachment, at a smaller size or fewer frames.
func TestARecordingFitsInOneAttachment(t *testing.T) {
	t.Parallel()
	var frames []session.Image
	for i := range 24 {
		rng := rand.New(rand.NewSource(int64(i)))
		frames = append(frames, pngFrame(t, 1280, 800, func(int, int) color.Color {
			return color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255}
		}))
	}
	b, ok := recording(frames)
	if !ok {
		t.Fatal("no recording fitted")
	}
	if len(b) > core.MaxAttachmentBytes {
		t.Fatalf("recording is %d bytes", len(b))
	}
}

// The recording comes first among QA's files, and the stills keep their
// numbers.
func TestQAKeepsARecordingBesideItsLastScreenshots(t *testing.T) {
	t.Parallel()
	s := &screenshots{}
	for i := range core.MaxScreenshots + 2 {
		s.Saw(session.Event{Kind: "tool_completed", Images: []session.Image{pngFrame(t, 64, 48, solid(color.Gray{uint8(i * 30)}))}})
	}
	shots := s.files("QA")
	if len(shots.Files) != core.MaxScreenshots+1 || shots.Files[0].Name != "recording.gif" || shots.Files[1].Name != "screenshot-1.png" || shots.Omitted != 2 {
		t.Fatalf("%d files, first %q then %q, %d omitted", len(shots.Files), shots.Files[0].Name, shots.Files[1].Name, shots.Omitted)
	}
	anim, err := gif.DecodeAll(bytes.NewReader(shots.Files[0].Data))
	if err != nil || len(anim.Image) != core.MaxScreenshots+2 {
		t.Fatalf("the recording should hold every screenshot: %v", err)
	}
}
