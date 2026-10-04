package upgrade

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/statepath"
	"github.com/shhac/crew-assistant/internal/upgradestate"
)

// The persisted record types live in upgradestate so the domain store can
// read and write them without importing the installer and its locks.
type (
	Step      = upgradestate.Step
	Backups   = upgradestate.Backups
	WaitingOn = upgradestate.WaitingOn
	Record    = upgradestate.Record
)

const (
	Draining           = upgradestate.Draining
	BackingUp          = upgradestate.BackingUp
	BackupFailed       = upgradestate.BackupFailed
	Installing         = upgradestate.Installing
	InstallFailed      = upgradestate.InstallFailed
	HandingOver        = upgradestate.HandingOver
	Probation          = upgradestate.Probation
	Healthy            = upgradestate.Healthy
	RollingBack        = upgradestate.RollingBack
	RolledBack         = upgradestate.RolledBack
	Abandoned          = upgradestate.Abandoned
	StoppedInProbation = upgradestate.StoppedInProbation
)

// CanonicalPath resolves aliases, including ancestors of new destinations.
// Locks, journals and restored files must all use the same identity.
func CanonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absolute)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(absolute), target)
		}
		return CanonicalPath(target)
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = CanonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

func RecordPath(state string) string {
	absolute, err := CanonicalPath(state)
	if err != nil {
		absolute = filepath.Clean(state)
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(absolute)))
	return filepath.Join(filepath.Dir(absolute), "upgrade", identity[:16], "record.json")
}

func ReadRecord(path string) (*Record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("read upgrade record: %w", err)
	}
	if !validStep(r.Step) {
		return nil, fmt.Errorf("unknown upgrade step %q", r.Step)
	}
	return &r, nil
}

func validStep(s Step) bool {
	switch s {
	case Draining, BackingUp, BackupFailed, Installing, InstallFailed, HandingOver, Probation, Healthy, RollingBack, RolledBack, Abandoned, StoppedInProbation:
		return true
	}
	return false
}

// WriteRecord syncs both the file and its directory before returning. Callers
// must persist the next step before performing its side effects.
func WriteRecord(path string, r Record) error {
	release, err := LockRecord(path)
	if err != nil {
		return err
	}
	defer release()
	return writeRecord(path, r)
}
func writeRecord(path string, r Record) error {
	if !validStep(r.Step) {
		return fmt.Errorf("unknown upgrade step %q", r.Step)
	}
	if err := durableMkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = statepath.WriteFileAtomic(path, b); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Restore requires a closed store and exclusive ownership of its lock. It is
// repeatable after a crash: backups remain intact until probation succeeds.
// Both backups are staged before touching either destination. Database size
// does not determine the amount of memory needed to recover.
func Restore(state, config string, backups Backups) error {
	return RestoreWithCheckpoint(state, config, backups, nil)
}

// RestoreWithCheckpoint permits synthetic interruption at synced file boundaries.
func RestoreWithCheckpoint(state, config string, backups Backups, checkpoint func(string) error) error {
	var err error
	state, err = CanonicalPath(state)
	if err != nil {
		return err
	}
	config, err = CanonicalPath(config)
	if err != nil {
		return err
	}
	s, err := stageCopy(backups.State, state, 0600)
	if err != nil {
		return fmt.Errorf("read state backup: %w", err)
	}
	defer os.Remove(s)
	c, err := stageCopy(backups.Config, config, 0600)
	if err != nil {
		return fmt.Errorf("read config backup: %w", err)
	}
	defer os.Remove(c)
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(state + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = os.Rename(s, state); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(state)); err != nil {
		return err
	}
	if checkpoint != nil {
		if err := checkpoint("state-restored"); err != nil {
			return err
		}
	}
	if err = os.Rename(c, config); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(config)); err != nil {
		return err
	}
	if checkpoint != nil {
		return checkpoint("config-restored")
	}
	return nil
}

// CopyFile syncs a private copy before replacing its destination. The source
// remains intact so a failed attempt or interrupted restore can be retried.
func CopyFile(source, destination string, mode os.FileMode) error {
	staged, err := stageCopy(source, destination, mode)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err = os.Rename(staged, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func stageCopy(source, destination string, mode os.FileMode) (name string, err error) {
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	if err = durableMkdirAll(filepath.Dir(destination)); err != nil {
		return "", err
	}
	out, err := os.CreateTemp(filepath.Dir(destination), ".upgrade-copy-*")
	if err != nil {
		return "", err
	}
	name = out.Name()
	defer func() {
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(name)
		}
	}()
	if err = out.Chmod(mode); err != nil {
		return name, err
	}
	if _, err = io.Copy(out, in); err != nil {
		return name, err
	}
	err = out.Sync()
	return name, err
}

// Sync newly created directory entries before publishing backup paths.
func durableMkdirAll(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := durableMkdirAll(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDirectory(parent)
}

// LockRecord serializes process transitions, separately from the lifetime state
// lock which cannot be acquired until a hung daemon has been killed.
func LockRecord(path string) (func(), error) {
	if err := durableMkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = lock.Unlock() }, nil
}
func EnsureDirectory(path string) error { return durableMkdirAll(path) }
