package gitrepo

import "golang.org/x/sys/unix"

// cloneTree clones a whole folder in one call on APFS: for a dependency
// folder of hundreds of thousands of files, seconds where cloning each file
// in turn takes minutes.
func cloneTree(from, to string) error {
	return unix.Clonefile(from, to, unix.CLONE_NOFOLLOW)
}
