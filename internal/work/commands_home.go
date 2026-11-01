package work

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// commandsHome is where hosted commands keep their private scratch. Every
// path a command makes starts here, its TMPDIR included, and Unix socket
// paths are limited to about a hundred bytes, so a home beside a deep state
// directory left a project's own tests unable to make a socket at all. On
// Unix it is a short directory of the owner's under /tmp, named for this
// state.
func (lp *Loop) commandsHome() string {
	state := lp.Core.StateDirectory()
	if runtime.GOOS == "windows" {
		return filepath.Join(state, "commands")
	}
	root := "/tmp"
	if runtime.GOOS == "darwin" {
		// The sandbox profile names real paths; /tmp is a link to it.
		root = "/private/tmp"
	}
	sum := sha256.Sum256([]byte(state))
	return filepath.Join(root, fmt.Sprintf("crew-%d-%x", os.Getuid(), sum[:3]))
}

// commandsHomes are the homes a sweep looks in for what earlier runs left:
// this one, and the one beside the state that versions up to v0.68.0 used.
func (lp *Loop) commandsHomes() []string {
	legacy := filepath.Join(lp.Core.StateDirectory(), "commands")
	if home := lp.commandsHome(); home != legacy {
		return []string{home, legacy}
	}
	return []string{legacy}
}

// privateHome makes home, or finds it, as a directory only its owner can
// use: under a shared /tmp someone else could have made it first.
func privateHome(home string) error {
	if err := os.Mkdir(home, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0o077 != 0 || !ownedByUs(st) {
		return fmt.Errorf("%s is not a private directory of yours; remove it and try again", home)
	}
	return nil
}
