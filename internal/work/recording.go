package work

import (
	"bytes"
	"image"
	"image/color/palette"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/session"
)

// maxRecordingFrames bounds what a turn's recording holds in memory and in
// the file: QA's most recent screenshots, in the order it took them.
const maxRecordingFrames = 60

// recordingWidths are tried widest first until the recording fits within
// one attachment; then every other frame is dropped, and so on.
var recordingWidths = []int{960, 720, 540}

// recording is an animated GIF of QA's turn, built from its screenshots so
// the owner can watch a flow work or break without running it again. It
// gives false when there are fewer than two frames it can read, or no size
// that fits within an attachment.
func recording(frames []session.Image) ([]byte, bool) {
	var decoded []image.Image
	for _, f := range frames {
		if img, _, err := image.Decode(bytes.NewReader(f.Data)); err == nil {
			decoded = append(decoded, img)
		}
	}
	if len(decoded) < 2 {
		return nil, false
	}
	for stride := 1; len(decoded)/stride >= 2; stride *= 2 {
		for _, width := range recordingWidths {
			if b, ok := encodeRecording(decoded, width, stride); ok {
				return b, true
			}
		}
	}
	return nil, false
}

func encodeRecording(frames []image.Image, width, stride int) ([]byte, bool) {
	anim := &gif.GIF{}
	for i := 0; i < len(frames); i += stride {
		anim.Image = append(anim.Image, frame(frames[i], width))
		anim.Delay = append(anim.Delay, 100)
	}
	// The final state stays up longer, so the result is easy to see.
	anim.Delay[len(anim.Delay)-1] = 300
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, anim); err != nil || b.Len() > core.MaxAttachmentBytes {
		return nil, false
	}
	return b.Bytes(), true
}

// frame scales src to at most width across and maps it to a fixed palette,
// so every frame of a recording shares one.
func frame(src image.Image, width int) *image.Paletted {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w > width {
		w, h = width, max(1, h*width/w)
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, bounds, draw.Src, nil)
	out := image.NewPaletted(scaled.Bounds(), palette.Plan9)
	draw.FloydSteinberg.Draw(out, out.Bounds(), scaled, image.Point{})
	return out
}
