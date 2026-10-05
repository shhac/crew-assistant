//go:build !darwin

package gitrepo

import "errors"

func cloneTree(from, to string) error {
	return errors.New("no whole-folder clones here")
}
