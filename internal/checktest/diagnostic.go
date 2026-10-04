package checktest

import (
	"strings"
	"unicode/utf8"
)

// Preserve the beginning (identity/reason) and end (failure evidence) as chunks
// arrive, rather than repeatedly copying an ever-growing test log.
type diagnostic struct {
	head      string
	tail      Tail
	size      int
	truncated bool
}

func (d *diagnostic) Write(text string) {
	d.size += len(text)
	if len(d.head) < 1024 {
		n := min(1024-len(d.head), len(text))
		for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		d.head += strings.Clone(text[:n])
	}
	d.tail.Write([]byte(text))
	d.truncated = d.size > 4<<10
}
func (d *diagnostic) String() string {
	if d == nil {
		return ""
	}
	if !d.truncated {
		return d.tail.String()
	}
	return d.head + "\n" + d.tail.String()
}
func bounded(s string, limit int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= limit {
		return strings.Clone(s)
	}
	const marker = "\n[diagnostic shortened]\n"
	n := (limit - len(marker)) / 2
	end := len(s) - (limit - len(marker) - n)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return strings.Clone(s[:n]) + marker + strings.Clone(s[end:])
}
