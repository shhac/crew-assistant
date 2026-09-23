package managedworkers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/workerbroker"
)

const testImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeProvisioner struct {
	prepares, checks int
	wait             bool
	started          chan struct{}
}

func (f *fakeProvisioner) Prepare(ctx context.Context, root string, previous *environment) (environment, error) {
	f.prepares++
	if f.wait {
		close(f.started)
		<-ctx.Done()
		return environment{}, ctx.Err()
	}
	return environment{Socket: "/synthetic/docker.sock", Image: testImage}, nil
}
func (f *fakeProvisioner) Check(context.Context, string, environment) error { f.checks++; return nil }

type fakeBroker struct {
	cfg     workerbroker.Config
	stopped atomic.Bool
	closed  bool
}

func (b *fakeBroker) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+b.cfg.AuthToken {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(404)
	})
}
func (b *fakeBroker) Run(ctx context.Context) error { <-ctx.Done(); b.stopped.Store(true); return nil }
func (b *fakeBroker) Close() error {
	if !b.stopped.Load() {
		return errors.New("closed before worker stopped")
	}
	b.closed = true
	return nil
}
func TestPreparedBrokerPrivateAuthenticationAndRecovery(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	provision := &fakeProvisioner{}
	m.runtime = provision
	var opened []*fakeBroker
	m.open = func(c workerbroker.Config) (broker, error) {
		b := &fakeBroker{cfg: c}
		opened = append(opened, b)
		return b, nil
	}
	model := config.Default().WorkerModel
	client, err := m.Prepare(context.Background(), "project/path", workspace, model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Find(context.Background(), "dispatch"); !errors.Is(err, worker.ErrNotFound) {
		t.Fatalf("private client authentication: %v", err)
	}
	if provision.prepares != 1 || provision.checks != 0 || len(opened) != 1 {
		t.Fatal("unexpected setup")
	}
	cfg := opened[0].cfg
	if cfg.AuthToken == "" || cfg.TokenEnv != "" || cfg.Engine != model.Engine || cfg.Model != model.Model || cfg.Effort != model.Effort {
		t.Fatal("broker profile/auth missing")
	}
	if _, err = m.Client(context.Background(), "project/path", workspace, model); err != nil {
		t.Fatal(err)
	}
	changedModel := model
	changedModel.Effort = "low"
	if _, err = m.Client(context.Background(), "project/path", workspace, changedModel); err == nil {
		t.Fatal("silently retained changed worker model")
	}
	if err = os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.com/changed\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Prepare(context.Background(), "project/path", workspace, model); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("stale preparation claimed success: %v", err)
	}
	os.Remove(filepath.Join(workspace, "go.mod"))
	if len(opened) != 1 {
		t.Fatal("same project opened twice")
	}
	if _, err = m.Client(context.Background(), "project/path", t.TempDir(), model); err == nil {
		t.Fatal("accepted replacement source folder")
	}
	entries, _ := os.ReadDir(m.root)
	if len(entries) != 1 || strings.Contains(entries[0].Name(), "project") {
		t.Fatal("project identity used as path")
	}
	raw, err := os.ReadFile(filepath.Join(m.root, entries[0].Name(), "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), cfg.AuthToken) || strings.Contains(string(raw), "endpoint") {
		t.Fatal("private connection leaked into manifest")
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	if !opened[0].closed {
		t.Fatal("broker lock not released")
	}
	if _, err = m.Client(context.Background(), "project/path", workspace, model); err == nil {
		t.Fatal("closed manager restarted")
	}
	m2, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	p2 := &fakeProvisioner{}
	m2.runtime = p2
	m2.open = func(c workerbroker.Config) (broker, error) {
		if c.AuthToken == cfg.AuthToken {
			t.Fatal("token reused across restart")
		}
		if c.StateDir != cfg.StateDir {
			t.Fatal("broker state lost")
		}
		return &fakeBroker{cfg: c}, nil
	}
	if _, err = m2.Client(context.Background(), "project/path", workspace, model); err != nil {
		t.Fatal(err)
	}
	if p2.prepares != 0 || p2.checks != 1 {
		t.Fatal("recovery installed tools")
	}
}
func TestRecoveryDoesNotPrepareAndShutdownCancelsPreparation(t *testing.T) {
	m, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeProvisioner{wait: true, started: make(chan struct{})}
	m.runtime = f
	if _, err = m.Client(context.Background(), "unprepared", t.TempDir(), config.Default().WorkerModel); err == nil {
		t.Fatal("recovered unprepared worker")
	}
	if f.prepares != 0 {
		t.Fatal("implicit preparation")
	}
	done := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		_, err := m.Prepare(context.Background(), "new", workspace, config.Default().WorkerModel)
		done <- err
	}()
	<-f.started
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel preparation")
	}
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare error %v", err)
	}
}
func TestTokensNeverSerialize(t *testing.T) {
	for _, v := range []any{workerbroker.Config{AuthToken: "internal-secret"}, worker.Config{AuthToken: "internal-secret"}} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "internal-secret") {
			t.Fatal("credential serialized")
		}
	}
}
