package work

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

// commandsHome is where hosted commands keep their private scratch. Every
// path a command makes starts here, its TMPDIR included, and Unix socket
// paths are limited to about a hundred bytes, so a home beside a deep state
// directory left a project's own tests unable to make a socket at all. On
// Unix it is a short directory of the owner's under /tmp, named for this
// state, unless /tmp could not be written: then the system's temporary
// directory, as inside another sandbox, where only that is writable.
func (lp *Loop) commandsHome() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(lp.Core.StateDirectory(), "commands")
	}
	if lp.tmpRefused.Load() {
		return lp.tempHome()
	}
	return lp.tmpHome()
}

func (lp *Loop) homeName() string {
	sum := sha256.Sum256([]byte(lp.Core.StateDirectory()))
	return fmt.Sprintf("crew-%d-%x", os.Getuid(), sum[:3])
}

func (lp *Loop) tmpHome() string {
	root := lp.tmpRoot
	if root == "" {
		root = "/tmp"
		if runtime.GOOS == "darwin" {
			// The sandbox profile names real paths; /tmp is a link to it.
			root = "/private/tmp"
		}
	}
	return filepath.Join(root, lp.homeName())
}

func (lp *Loop) tempHome() string { return filepath.Join(os.TempDir(), lp.homeName()) }

// makeCommandsHome makes the home a real sandbox opens in, moving to the
// system's temporary directory for good if /tmp refuses it.
func (lp *Loop) makeCommandsHome() (string, error) {
	home := lp.commandsHome()
	err := privateHome(home)
	if errors.Is(err, fs.ErrPermission) && runtime.GOOS != "windows" && !lp.tmpRefused.Load() {
		lp.tmpRefused.Store(true)
		home = lp.tempHome()
		err = privateHome(home)
	}
	return home, err
}

// commandsHomes are the homes a sweep looks in for what earlier runs left:
// this one, the other one it may have used, and the one beside the state that
// versions up to v0.68.0 used.
func (lp *Loop) commandsHomes() []string {
	var homes []string
	for _, home := range []string{lp.commandsHome(), lp.tmpHome(), lp.tempHome(), filepath.Join(lp.Core.StateDirectory(), "commands")} {
		if !slices.Contains(homes, home) {
			homes = append(homes, home)
		}
	}
	if runtime.GOOS == "windows" {
		return homes[:1]
	}
	return homes
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
