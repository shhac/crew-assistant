package statepath

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data only its owner can read. The data
// is written and synced to a temporary file beside it before the rename, so a
// reader never sees it half written and a looser mode on the old file is not
// kept.
func WriteFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = writeAndClose(f, data); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeAndClose(f *os.File, data []byte) error {
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
