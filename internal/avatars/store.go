// Package avatars keeps the pictures Codex draws for the assistant and the
// team, each at the three sizes the dashboard shows them.
package avatars

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"

	"golang.org/x/image/draw"
)

// Sizes are the pictures kept for each avatar, in pixels. The two smaller
// ones are twice the size they are shown at, for sharp screens: a card or
// the chat header, and inline beside text or as the tab's icon.
var Sizes = map[string]int{"large": 512, "medium": 128, "small": 48}

// A drawing is a PNG of a sensible size. Codex's are about 1254 pixels a
// side; the bounds keep a hostile file from costing much memory to decode.
const (
	maxBytes = 25 << 20
	maxSide  = 2048
	minSide  = 256
)

var validID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID reports whether id names a stored avatar, so it can go in a path.
func ValidID(id string) bool { return validID.MatchString(id) }

type Store struct{ dir string }

func NewStore(stateDirectory string) Store {
	return Store{dir: filepath.Join(stateDirectory, "avatars")}
}

// Put keeps a picture and returns its id. It is decoded and drawn afresh at
// each size, so nothing but pixels survives from what a model produced.
func (s Store) Put(data []byte) (string, error) {
	if len(data) > maxBytes {
		return "", errors.New("the picture is larger than 25 MB")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("the picture could not be read: %w", err)
	}
	if cfg.Width > maxSide || cfg.Height > maxSide || cfg.Width < minSide || cfg.Height < minSide {
		return "", fmt.Errorf("the picture is %d×%d; it must be between %d and %d pixels a side", cfg.Width, cfg.Height, minSide, maxSide)
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("the picture could not be read: %w", err)
	}
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:16])
	final := filepath.Join(s.dir, id)
	if _, err := os.Stat(final); err == nil {
		return id, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(s.dir, ".put-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	square := centreSquare(src.Bounds())
	for name, side := range Sizes {
		dst := image.NewRGBA(image.Rect(0, 0, side, side))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, square, draw.Src, nil)
		var out bytes.Buffer
		if err := png.Encode(&out, dst); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(tmp, name+".png"), out.Bytes(), 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, final); err != nil && !errors.Is(err, os.ErrExist) {
		if _, statErr := os.Stat(final); statErr != nil {
			return "", err
		}
	}
	return id, nil
}

// Path is where one size of an avatar is kept, if the id and size are ones
// the store could have made.
func (s Store) Path(id, size string) (string, bool) {
	if _, ok := Sizes[size]; !ok || !ValidID(id) {
		return "", false
	}
	return filepath.Join(s.dir, id, size+".png"), true
}

func centreSquare(r image.Rectangle) image.Rectangle {
	side := min(r.Dx(), r.Dy())
	x := r.Min.X + (r.Dx()-side)/2
	y := r.Min.Y + (r.Dy()-side)/2
	return image.Rect(x, y, x+side, y+side)
}
