package gitrepo

import (
	"context"
	"strings"
)

// BuildIncludes reads the owner's repository without fetching or running hooks.
// A squashed landing is identified by its trailer; a preserved landing by ancestry.
func BuildIncludes(ctx context.Context, source, build, revision, marker string) (bool, error) {
	// Try the trailer even if the source no longer holds the task's original commit.
	out, err := run(ctx, source, "log", "--fixed-strings", "--grep="+marker, "--format=%B%x00", build, "--")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == marker {
			return true, nil
		}
	}
	return isAncestor(ctx, source, revision, build)
}
