package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup takes a consistent SQLite snapshot while the store is still open.
// VACUUM INTO includes WAL pages. The target must be a fresh private file.
func (s *Store) Backup(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(path, "'", "''")+"'"); err != nil {
		return fmt.Errorf("snapshot state: %w", err)
	}
	f, err = os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
