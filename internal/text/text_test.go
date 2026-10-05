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

func TestTailKeepsTheEndOnWholeLines(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		b.WriteString(strings.Repeat("é", 20) + string(rune('a'+i%26)) + "\n")
	}
	got := Tail(b.String(), 100, 1000)
	if len(got) > 1000 || !utf8.ValidString(got) || strings.Count(got, "\n") > 99 || !strings.HasSuffix(got, "n") || !strings.HasPrefix(got, "é") {
		t.Fatalf("Tail gave %d bytes:\n%s", len(got), got)
	}
	if Tail("one\ntwo\nthree\n", 2, 100) != "two\nthree" {
		t.Fatal("a short tail was changed")
	}
	if got := Tail(strings.Repeat("x", 50), 10, 20); got != strings.Repeat("x", 20) {
		t.Fatalf("one long line: %q", got)
	}
}

func TestRedactHidesWhatLooksLikeACredential(t *testing.T) {
	in := "push https://user:hunter2@github.com/o/r.git\nAuthorization: Bearer abc.def\ntoken ghp_abcdefghij1234 and github_pat_11ABC_def and sk-abcdefghijklmnopqrstu and AKIAABCDEFGHIJKLMNOP\nnothing secret here"
	got := Redact(in)
	for _, secret := range []string{"hunter2", "abc.def", "ghp_", "github_pat_", "sk-abc", "AKIA"} {
		if strings.Contains(got, secret) {
			t.Fatalf("%q left in:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "nothing secret here") {
		t.Fatalf("ordinary text was redacted:\n%s", got)
	}
}
