package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipNeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("é", 10) // two bytes each
	for limit := 1; limit < len(s); limit++ {
		if got := Clip(s, limit); !utf8.ValidString(got) {
			t.Fatalf("Clip(%d) = %q is not valid UTF-8", limit, got)
		}
	}
	if Clip("short", 10) != "short" || Short("abcdef1234") != "abcdef1" || Short("abc") != "abc" {
		t.Fatal("text that fit was changed")
	}
}
