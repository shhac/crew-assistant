package gitrepo

import (
	"context"
	"path"
	"strings"
)

// isPattern says whether a prepare entry names folders by pattern rather
// than by path: * and ? match within one folder name, and ** any number of
// folders, so **/node_modules is every node_modules a workspace has.
func isPattern(entry string) bool { return strings.ContainsAny(entry, "*?[") }

// preparedPaths are the paths r.prepare names in the owner's checkout:
// each path as given, and for each pattern, the ignored files and folders
// there that it matches. Only what git ignores is ever matched, so a
// pattern can't bring in the owner's tracked or uncommitted work.
func (r Repo) preparedPaths(ctx context.Context) ([]string, error) {
	var paths, patterns []string
	for _, entry := range r.prepare {
		if isPattern(entry) {
			patterns = append(patterns, entry)
			continue
		}
		paths = append(paths, entry)
	}
	if len(patterns) == 0 {
		return paths, nil
	}
	out, err := run(ctx, r.source, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	for _, ignored := range strings.Split(out, "\x00") {
		ignored = strings.TrimSuffix(ignored, "/")
		if ignored == "" {
			continue
		}
		for _, pattern := range patterns {
			if matchPath(strings.Split(pattern, "/"), strings.Split(ignored, "/")) {
				paths = append(paths, ignored)
				break
			}
		}
	}
	return paths, nil
}

// matchPath matches a path's folder names against a pattern's, where **
// stands for any number of them.
func matchPath(pattern, names []string) bool {
	if len(pattern) == 0 {
		return len(names) == 0
	}
	if pattern[0] == "**" {
		for skip := 0; skip <= len(names); skip++ {
			if matchPath(pattern[1:], names[skip:]) {
				return true
			}
		}
		return false
	}
	if len(names) == 0 {
		return false
	}
	ok, _ := path.Match(pattern[0], names[0])
	return ok && matchPath(pattern[1:], names[1:])
}
