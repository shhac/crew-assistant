package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresetAvatarsDrawOnlyTheirOwnValues(t *testing.T) {
	for _, shape := range []string{"orb", "spark", "leaf"} {
		svg, err := Avatar{Shape: shape, Background: "#123456", Accent: "#abcdef"}.SVG()
		if err != nil || !strings.Contains(svg, `fill="#123456"`) || !strings.Contains(svg, "#abcdef") || strings.Contains(svg, "EXTRA") || strings.Contains(svg, "%!") {
			t.Fatal(shape, svg, err)
		}
	}
	for _, avatar := range []Avatar{{Shape: "script", Background: "#123456", Accent: "#abcdef"}, {Shape: "orb", Background: "url(https://example.com)", Accent: "#abcdef"}} {
		if _, err := avatar.SVG(); err == nil {
			t.Fatal("untrusted graphic accepted", avatar)
		}
	}
}

func TestADrawnAvatarIsPathDataAndNothingElse(t *testing.T) {
	drawn := func(d string) Avatar {
		return Avatar{Background: "#101820", Accent: "#f2aa4c", Marks: []Mark{{D: d, Color: "#f2aa4c"}}}
	}
	svg, err := drawn("M 20 64\n\tC 20 30, 108 30, 108 64 Z").SVG()
	if err != nil || !strings.Contains(svg, `<path d="M 20 64 C 20 30, 108 30, 108 64 Z" fill="#f2aa4c"/>`) {
		t.Fatalf("a drawing with newlines should render normalised: %s %v", svg, err)
	}
	stroked, err := Avatar{Background: "#101820", Accent: "#f2aa4c", Marks: []Mark{{D: "M10 10L118 118", Color: "#ffffff", StrokeWidth: 6.5}}}.SVG()
	if err != nil || !strings.Contains(stroked, `fill="none" stroke="#ffffff" stroke-width="6.5"`) {
		t.Fatalf("a stroked mark: %s %v", stroked, err)
	}
	for _, d := range []string{
		`M0 0" onload="alert(1)`,
		"M0 0<script>",
		"M0 0 url(#x)",
		"M0 0 &amp;",
		"L10 10",
		"",
		"M" + strings.Repeat("1 ", 400),
		"M0 0 X 10",
	} {
		if _, err := drawn(d).SVG(); err == nil {
			t.Errorf("path %q was accepted", d)
		}
	}
	for _, bad := range []Avatar{
		{Background: "#101820", Accent: "#f2aa4c", Marks: []Mark{{D: "M0 0", Color: "red"}}},
		{Background: "#101820", Accent: "#f2aa4c", Marks: []Mark{{D: "M0 0", Color: "#ffffff", StrokeWidth: 40}}},
		{Background: "#101820", Accent: "#f2aa4c", Marks: make([]Mark, 9)},
	} {
		if err := bad.Normalized().Validate(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestAnAvatarWithoutMarksStillLoads(t *testing.T) {
	var a Avatar
	if err := json.Unmarshal([]byte(`{"shape":"orb","background":"#16211e","accent":"#a8c5a8"}`), &a); err != nil || a.Validate() != nil {
		t.Fatal(a, err)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "marks") {
		t.Fatalf("an undrawn avatar should save as before: %s", raw)
	}
}
