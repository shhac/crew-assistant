package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Avatar is a small vector picture: a coloured square with either one of
// the preset shapes or up to eight marks the assistant drew itself. Only
// validated numbers, colours and path commands ever reach the SVG, so a
// drawing cannot carry markup, links or scripts.
type Avatar struct {
	Shape      string `json:"shape"`
	Background string `json:"background"`
	Accent     string `json:"accent"`
	Marks      []Mark `json:"marks,omitempty"`
}

// Mark is one path on a 128-unit square. A zero stroke width fills it.
type Mark struct {
	D           string  `json:"d"`
	Color       string  `json:"color"`
	StrokeWidth float64 `json:"stroke_width"`
}

const maxMarks = 8

var (
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	pathData = regexp.MustCompile(`^[Mm][MmLlHhVvCcSsQqTtAaZz0-9eE.,\- ]*$`)
	spaces   = regexp.MustCompile(`\s+`)
)

// Normalized collapses the whitespace models put in path data, so a drawing
// is judged on what it draws.
func (a Avatar) Normalized() Avatar {
	if len(a.Marks) == 0 {
		return a
	}
	marks := make([]Mark, len(a.Marks))
	for i, m := range a.Marks {
		m.D = strings.TrimSpace(spaces.ReplaceAllString(m.D, " "))
		marks[i] = m
	}
	a.Marks = marks
	return a
}

func (a Avatar) Validate() error {
	if !hexColor.MatchString(a.Background) || !hexColor.MatchString(a.Accent) {
		return errors.New("colors must be #RRGGBB")
	}
	if len(a.Marks) == 0 {
		if _, ok := presets[a.Shape]; !ok {
			return errors.New("shape must be orb, spark or leaf, or the avatar must be drawn with marks")
		}
		return nil
	}
	if len(a.Marks) > maxMarks {
		return fmt.Errorf("at most %d marks", maxMarks)
	}
	for i, m := range a.Marks {
		if len(m.D) == 0 || len(m.D) > 600 || !pathData.MatchString(m.D) {
			return fmt.Errorf("mark %d must be SVG path data (M, L, C, Q, A, Z and numbers) of at most 600 characters, starting with M", i+1)
		}
		if !hexColor.MatchString(m.Color) {
			return fmt.Errorf("mark %d color must be #RRGGBB", i+1)
		}
		if m.StrokeWidth < 0 || m.StrokeWidth > 16 {
			return fmt.Errorf("mark %d stroke width must be 0 to 16", i+1)
		}
	}
	return nil
}

var presets = map[string]string{
	"orb":   `<circle cx="64" cy="64" r="29" fill="%[1]s"/><ellipse cx="64" cy="64" rx="48" ry="17" fill="none" stroke="%[1]s" stroke-width="5" transform="rotate(-30 64 64)"/>`,
	"spark": `<path d="M64 18 75 49 106 64 75 78 64 110 50 78 20 64 50 49Z" fill="%[1]s"/><circle cx="96" cy="28" r="6" fill="%[1]s"/>`,
	"leaf":  `<path d="M28 92C18 39 65 21 106 23 108 77 76 108 28 92Z" fill="%[1]s"/><path d="M31 91 83 43" fill="none" stroke="%[2]s" stroke-width="6" stroke-linecap="round"/>`,
}

// SVG draws the avatar. It refuses anything Validate refuses.
func (a Avatar) SVG() (string, error) {
	a = a.Normalized()
	if err := a.Validate(); err != nil {
		return "", err
	}
	var body strings.Builder
	if len(a.Marks) == 0 {
		fmt.Fprintf(&body, presets[a.Shape], a.Accent, a.Background)
	}
	for _, m := range a.Marks {
		if m.StrokeWidth == 0 {
			fmt.Fprintf(&body, `<path d="%s" fill="%s"/>`, m.D, m.Color)
			continue
		}
		fmt.Fprintf(&body, `<path d="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round"/>`, m.D, m.Color, strconv.FormatFloat(m.StrokeWidth, 'f', -1, 64))
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128"><rect width="128" height="128" rx="32" fill="%s"/>%s</svg>`, a.Background, body.String()), nil
}
