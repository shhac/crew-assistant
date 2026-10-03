// Package releaseversion parses complete semver tags and preserves tag style.
package releaseversion

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

var strict = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func Valid(v string) bool {
	return strict.MatchString(v) && semver.IsValid("v"+strings.TrimPrefix(v, "v"))
}
func Compare(a, b string) int {
	return semver.Compare("v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v"))
}
func Next(proposed, latest string) (string, error) {
	if !Valid(proposed) || (latest != "" && Compare(proposed, latest) <= 0) {
		return "", errors.New("release version must be strict semver above the latest version tag")
	}
	prefix := "v"
	if latest != "" && !strings.HasPrefix(latest, "v") {
		prefix = ""
	}
	return prefix + strings.TrimPrefix(proposed, "v"), nil
}
