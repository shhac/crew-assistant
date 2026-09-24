// Package text shortens text for people to read and for places with limits:
// commit messages, pull requests, prompts and the assistant's view of state.
package text

import (
	"strings"
	"unicode/utf8"
)

// Short is the abbreviated form of a commit id.
func Short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Clip shortens text to about limit bytes without splitting a character, so
// what it writes into commit messages and pull requests stays valid UTF-8.
func Clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return strings.TrimSpace(text[:cut]) + "…"
}
