package toolkit

import "regexp"

// secretShapes are token-shaped strings the family's services and Homebrew
// use, plus credentials in headers, URLs and key=value pairs.
var secretShapes = regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{8,}` +
	`|github_pat_[A-Za-z0-9_]+` +
	`|xox[abcdeoprs]-[A-Za-z0-9-]+|xapp-[A-Za-z0-9-]+` +
	`|lin_(?:api|oauth)_[A-Za-z0-9_-]+` +
	`|(?:sk|rk|pk)_(?:live|test)_[A-Za-z0-9]+` +
	`|(?:ntn|secret)_[A-Za-z0-9]{20,}` +
	`|phx_[A-Za-z0-9]{20,}` +
	`|AKIA[0-9A-Z]{16}` +
	`|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}` +
	`|(?i:authorization:[^\r\n]+)` +
	`|(?i:bearer\s+[A-Za-z0-9._~+/=-]{8,})` +
	`|https?://[^/\s:@]*:[^@\s]+@` +
	`|(?i:(?:token|secret|password|passwd|api[_-]?key)["']?\s*[:=]\s*["']?[^\s"',]{4,})`)

// Redact replaces anything token-shaped in a line of output.
func Redact(line string) string {
	return secretShapes.ReplaceAllString(line, "[redacted]")
}
