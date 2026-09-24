package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store serializes mutations in-process and uses BEGIN IMMEDIATE to serialize
// other processes. Each mutation atomically records entities and audit events.
// stateSchema is the version of the state model this build reads and writes.
// Version 2 is the project-teams model; version 1 (unversioned) was the worker
// model it replaced.
const stateSchema = 2

// ErrStateSchema means the state file was written for a different model.
var ErrStateSchema = errors.New("state file was written by an incompatible version; convert it before starting")

type Store struct {
	db             *sql.DB
	mu             sync.Mutex
	stateDirectory string
	temporaryState bool
}
type diskState struct {
	// Schema names the state model that wrote this document. There is no reader
	// for another model: state from before a clean break is converted once,
	// outside the daemon, rather than silently reinterpreted.
	Schema            int             `json:"schema"`
	ChatCheckpoint    ChatCheckpoint  `json:"chat_checkpoint,omitempty"`
	ChatTurns         []ChatTurn      `json:"chat_turns,omitempty"`
	ChatHold          *ChatHold       `json:"chat_hold,omitempty"`
	ChatQueueRevision int             `json:"chat_queue_revision,omitempty"`
	Events            map[string]bool `json:"events"`
	Snapshot          Snapshot        `json:"snapshot"`
	ModelCalls        map[string]int  `json:"model_calls"`
}

func Open(path string) (*Store, error) {
	var stateDirectory string
	temporaryState := path == ":memory:"
	if temporaryState {
		var err error
		stateDirectory, err = os.MkdirTemp("", "crew-assistant-state-")
		if err != nil {
			return nil, err
		}
		canonical, err := filepath.EvalSymlinks(stateDirectory)
		if err != nil {
			os.RemoveAll(stateDirectory)
			return nil, err
		}
		stateDirectory = canonical
		defer func() {
			if temporaryState {
				os.RemoveAll(stateDirectory)
			}
		}()
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		f.Close()
		if err = os.Chmod(path, 0600); err != nil {
			return nil, err
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		path, err = filepath.Abs(canonical)
		if err != nil {
			return nil, err
		}
		stateDirectory = filepath.Dir(path)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS state (id INTEGER PRIMARY KEY CHECK(id=1), payload TEXT NOT NULL, version INTEGER NOT NULL DEFAULT 1);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db, stateDirectory: stateDirectory, temporaryState: temporaryState}
	if err = s.update(context.Background(), func(v *Snapshot) error {
		for i := range v.Projects {
			if err := s.prepareProject(&v.Projects[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	temporaryState = false
	return s, nil
}
func (s *Store) Close() error {
	err := s.db.Close()
	if s.temporaryState {
		return errors.Join(err, os.RemoveAll(s.stateDirectory))
	}
	return err
}
func emptyState() Snapshot {
	return Snapshot{Projects: []Project{}, Tasks: []Task{}, Decisions: []Decision{}, Messages: []Message{}, Memories: []Memory{}, Activity: []Activity{}, Integrations: []Integration{}, Members: []Member{}, Events: map[string]bool{}, ModelCalls: map[string]int{}}
}
func readState(ctx context.Context, conn *sql.Conn) (Snapshot, error) {
	var data string
	err := conn.QueryRowContext(ctx, "SELECT payload FROM state WHERE id=1").Scan(&data)
	if err == sql.ErrNoRows {
		return emptyState(), nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	d := diskState{Snapshot: emptyState()}
	if err = json.Unmarshal([]byte(data), &d); err != nil {
		return Snapshot{}, fmt.Errorf("decode durable state: %w", err)
	}
	if d.Schema != stateSchema {
		return Snapshot{}, fmt.Errorf("%w: found schema %d, this build reads %d", ErrStateSchema, d.Schema, stateSchema)
	}
	d.Snapshot.ChatCheckpoint = d.ChatCheckpoint
	d.Snapshot.ChatTurns = d.ChatTurns
	d.Snapshot.ChatHold = d.ChatHold
	d.Snapshot.ChatQueueRevision = d.ChatQueueRevision
	d.Snapshot.Events = d.Events
	if d.Snapshot.Events == nil {
		d.Snapshot.Events = map[string]bool{}
	}
	d.Snapshot.ModelCalls = d.ModelCalls
	if d.Snapshot.ModelCalls == nil {
		d.Snapshot.ModelCalls = map[string]int{}
	}
	deriveStages(&d.Snapshot)
	return d.Snapshot, nil
}
func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer conn.Close()
	return readState(ctx, conn)
}
func (s *Store) update(ctx context.Context, fn func(*Snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	state, err := readState(ctx, conn)
	if err != nil {
		return err
	}
	if err = fn(&state); err != nil {
		return err
	}
	deriveStages(&state)
	// A task's status can pass through a value within one change; checking
	// here, not by polling, means a wake waiting on it never misses it.
	settleTaskWakes(&state, time.Now().UTC())
	data, err := json.Marshal(diskState{Schema: stateSchema, ChatCheckpoint: state.ChatCheckpoint, ChatTurns: state.ChatTurns, ChatHold: state.ChatHold, ChatQueueRevision: state.ChatQueueRevision, Snapshot: state, ModelCalls: state.ModelCalls, Events: state.Events})
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO state(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,version=version+1", string(data)); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
