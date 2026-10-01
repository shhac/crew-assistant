package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// migrations upgrade a state document from the schema of its key to the
// next. Each works on the document as JSON, since the types it was written
// with are gone, and changes only what its version changed.
var migrations = map[int]func(doc map[string]any) error{}

// upgrade brings state written by an earlier schema up to this build's,
// once, keeping a copy of the database as it was beside it; state from a
// later schema is refused before anything reads it.
func (s *Store) upgrade(ctx context.Context, path string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := payload(ctx, conn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var head struct {
		Schema int `json:"schema"`
	}
	if err = json.Unmarshal([]byte(data), &head); err != nil {
		return fmt.Errorf("decode durable state: %w", err)
	}
	switch {
	case head.Schema == stateSchema:
		return nil
	case head.Schema > stateSchema:
		return fmt.Errorf("%w: it is schema %d, from a newer build, and this one reads %d; upgrade crew-assistant", ErrStateSchema, head.Schema, stateSchema)
	}
	upgraded, err := upgradeState([]byte(data), head.Schema, stateSchema, migrations)
	if err != nil {
		return err
	}
	// SQLite copies a database only outside a transaction, so the upgrade
	// below makes sure nothing wrote in between.
	if !s.temporaryState {
		if err = backUp(ctx, conn, path, head.Schema); err != nil {
			return err
		}
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if current, err := payload(ctx, conn); err != nil || current != data {
		return errors.Join(errors.New("state changed while it was being upgraded; start again"), err)
	}
	if _, err = conn.ExecContext(ctx, "UPDATE state SET payload=?, version=version+1 WHERE id=1", string(upgraded)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func payload(ctx context.Context, conn *sql.Conn) (string, error) {
	var data string
	err := conn.QueryRowContext(ctx, "SELECT payload FROM state WHERE id=1").Scan(&data)
	return data, err
}

// upgradeState runs the migrations from schema from up to schema to.
func upgradeState(data []byte, from, to int, migrations map[int]func(map[string]any) error) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	// Numbers stay exactly as written, never rounded through float64.
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode durable state: %w", err)
	}
	for v := from; v < to; v++ {
		migrate, ok := migrations[v]
		if !ok {
			return nil, fmt.Errorf("%w: no upgrade from schema %d to %d", ErrStateSchema, v, v+1)
		}
		if err := migrate(doc); err != nil {
			return nil, fmt.Errorf("upgrade state from schema %d: %w", v, err)
		}
	}
	doc["schema"] = to
	return json.Marshal(doc)
}

// backUp copies the database, as the schema it was written in, beside it.
func backUp(ctx context.Context, conn *sql.Conn, path string, schema int) error {
	dir, base := filepath.Split(path)
	backup := filepath.Join(dir, fmt.Sprintf("%s.schema-%d.%s.bak", base, schema, time.Now().UTC().Format("20060102T150405Z")))
	if _, err := conn.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return fmt.Errorf("back up state before upgrading it: %w", err)
	}
	return os.Chmod(backup, 0o600)
}
