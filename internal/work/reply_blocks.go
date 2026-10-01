package work

import "strings"

// splitBlock takes the last fenced block of a kind, such as ```wake, off a
// role's reply, returning the reply without it and the block's contents.
func splitBlock(text, kind string) (string, string) {
	fence := "```" + kind
	start := strings.LastIndex(text, fence+"\n")
	if start < 0 {
		return text, ""
	}
	rest := text[start+len(fence):]
	end := strings.Index(rest, "```")
	if end < 0 {
		return text, ""
	}
	return strings.TrimSpace(text[:start] + rest[end+3:]), strings.TrimSpace(rest[:end])
}
