// Package text shortens text for people to read and for places with limits:
// commit messages, pull requests, prompts and the assistant's view of state.
package text

import (
	"regexp"
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

// Tail keeps the last lines of text, then at most limit bytes of those,
// starting on a whole line where one fits: the end of a log, where a
// failure says what it was.
func Tail(text string, lines, limit int) string {
	text = strings.TrimRight(text, "\n")
	all := strings.Split(text, "\n")
	if len(all) > lines {
		text = strings.Join(all[len(all)-lines:], "\n")
	}
	if len(text) <= limit {
		return text
	}
	cut := len(text) - limit
	if nl := strings.IndexByte(text[cut:], '\n'); nl >= 0 && nl < len(text)-cut-1 {
		return text[cut+nl+1:]
	}
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}

// secrets are the shapes of credentials that output can echo: GitHub,
// cloud and chat tokens, authorization headers, and passwords in addresses.
var secrets = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|(?i:authorization:[^\r\n]+)|(?i:bearer\s+[A-Za-z0-9._~+/=-]{8,})|https?://[^/\s]*:[^@\s]+@|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]+|sk-[A-Za-z0-9_-]{16,}|lin_(?:api|oauth)_[A-Za-z0-9_-]+|glpat-[A-Za-z0-9_-]{16,}|npm_[A-Za-z0-9]{30,}`)

// Redact replaces anything shaped like a credential, so output can be shown
// to people and agents.
func Redact(text string) string { return secrets.ReplaceAllString(text, "[redacted]") }
