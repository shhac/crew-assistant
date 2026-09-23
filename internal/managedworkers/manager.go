// Package managedworkers owns local worker setup and broker lifetimes. Its
// preparation commands are fixed; neither models nor project files supply them.
package managedworkers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
	"github.com/shhac/crew-assistant/internal/statepath"
	"github.com/shhac/crew-assistant/internal/workerbroker"
)

type environment struct {
	Socket string `json:"socket"`
	Image  string `json:"image"`
}
type manifest struct {
	ProjectID   string      `json:"project_id"`
	Workspace   string      `json:"workspace"`
	Environment environment `json:"environment"`
}
type provisioner interface {
	Prepare(context.Context, string, *environment) (environment, error)
	Check(context.Context, string, environment) error
}
type broker interface {
	Handler() http.Handler
	Run(context.Context) error
	Close() error
}
type running struct {
	client    *worker.Client
	workspace string
	directory string
	model     config.Model
	cancel    context.CancelFunc
	server    *http.Server
	done      chan struct{}
	err       error
	broker    broker
}

type Manager struct {
	// Diagnostics is set before the manager is used; nil writes to stderr.
	Diagnostics *diagnostics.Logger
	// Admit and TokenBudget carry the daemon's live resource policy into every
	// broker it opens. They are read per request, so an owner changing a limit
	// does not require restarting a worker to apply it. Both are set before the
	// manager is used; nil leaves a broker with no configured resource policy.
	//
	// Admit receives the model the broker was actually opened with. A running
	// broker keeps its engine, binary and login until it is restarted, so the
	// account its headroom must be measured against is that one, not whatever
	// configuration now names.
	Admit        func(context.Context, string, config.Model) error
	TokenBudget  func() int64
	root         string
	mu           sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	closed       bool
	running      map[string]*running
	runtime      provisioner
	dependencies func(context.Context, string, string, string, environment) ([]workerbroker.DependencyMount, error)
	open         func(workerbroker.Config) (broker, error)
}

func New(stateRoot string) (*Manager, error) {
	root, err := statepath.EnsureDirectory(stateRoot, "managed-workers")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	local := newLocalRuntime()
	return &Manager{root: root, ctx: ctx, cancel: cancel, running: map[string]*running{}, runtime: local, dependencies: local.prepareDependencies, open: func(c workerbroker.Config) (broker, error) { return workerbroker.New(c) }}, nil
}

// Prepare may install the fixed free toolchain and download official base images.
// Calling it does not commission project work. Existing artifacts are retained.
func (m *Manager) Prepare(ctx context.Context, projectID, workspace string, model config.Model) (*worker.Client, error) {
	return m.client(ctx, projectID, workspace, model, true)
}

// Client lazily reconnects persisted brokers after daemon restart. It never
// installs software, starts virtual machines, builds images or downloads data.
func (m *Manager) Client(ctx context.Context, projectID, workspace string, model config.Model) (*worker.Client, error) {
	return m.client(ctx, projectID, workspace, model, false)
}
func (m *Manager) client(ctx context.Context, projectID, workspace string, model config.Model, prepare bool) (*worker.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || ctx.Err() != nil {
		return nil, errors.New("worker setup was stopped")
	}
	if projectID == "" {
		return nil, errors.New("choose a project before preparing its worker")
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err == nil {
		canonical, err = filepath.Abs(canonical)
	}
	if err != nil {
		return nil, errors.New("the project's local folder is unavailable; choose an existing folder")
	}
	if info, e := os.Stat(canonical); e != nil || !info.IsDir() {
		return nil, errors.New("the project's local folder must be a directory")
	}
	if r := m.running[projectID]; r != nil {
		if r.model != model {
			return nil, errors.New("this worker is using its previous model settings; restart the assistant to apply the selected worker model and recover its recorded work")
		}
		if r.workspace != canonical {
			return nil, errors.New("this project's worker belongs to another folder; retain that folder until its work is finished")
		}
		select {
		case <-r.done:
			return nil, errors.New("the project worker stopped unexpectedly; restart the assistant to recover its recorded work")
		default:
		}
		if prepare {
			if err := dependenciesCurrent(r.directory, canonical); err != nil {
				return nil, err
			}
		}
		return r.client, nil
	}
	sum := sha256.Sum256([]byte(projectID))
	dir, err := statepath.EnsureDirectory(m.root, hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, err
	}
	var saved manifest
	raw, err := os.ReadFile(filepath.Join(dir, "environment.json"))
	if err == nil {
		if json.Unmarshal(raw, &saved) != nil || saved.ProjectID != projectID || saved.Workspace != canonical {
			return nil, errors.New("saved worker setup does not match this project folder; preserve its state for recovery")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("cannot read saved worker setup")
	}
	if prepare {
		var previous *environment
		if saved.ProjectID != "" {
			previous = &saved.Environment
		}
		env, e := m.runtime.Prepare(ctx, m.root, previous)
		if e != nil {
			return nil, e
		}
		saved = manifest{ProjectID: projectID, Workspace: canonical, Environment: env}
		if e = saveManifest(dir, saved); e != nil {
			return nil, e
		}
	} else {
		if saved.ProjectID == "" {
			return nil, errors.New("this project's worker is not ready yet; ask the assistant to prepare it")
		}
		if err = m.runtime.Check(ctx, m.root, saved.Environment); err != nil {
			return nil, err
		}
	}
	var mounts []workerbroker.DependencyMount
	if prepare {
		mounts, err = m.dependencies(ctx, m.root, dir, canonical, saved.Environment)
	} else {
		mounts, err = loadDependencies(dir)
	}
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	brokerDir, err := statepath.EnsureDirectory(dir, "broker")
	if err != nil {
		return nil, err
	}
	var key [32]byte
	if _, err = rand.Read(key[:]); err != nil {
		return nil, errors.New("cannot create private worker authentication")
	}
	token := hex.EncodeToString(key[:])
	var admit func(context.Context) error
	if m.Admit != nil {
		opened := model
		admit = func(ctx context.Context) error { return m.Admit(ctx, projectID, opened) }
	}
	var command workerbroker.Commander
	if local, ok := m.runtime.(*localRuntime); ok {
		command = workerbroker.CommandFunc(func(ctx context.Context, args []string, input []byte) ([]byte, error) {
			return local.docker(ctx, m.root, saved.Environment.Socket, args, input)
		})
	}
	b, err := m.open(workerbroker.Config{Diagnostics: m.Diagnostics, Command: command, Dependencies: mounts, StateDir: brokerDir, Workspace: canonical, ProjectID: projectID, Image: saved.Environment.Image, DockerSocket: saved.Environment.Socket, AuthToken: token, Engine: model.Engine, Model: model.Model, Effort: model.Effort, CodexBin: model.CodexBin, CodexHome: model.CodexHome, ClaudeBin: model.ClaudeBin, ClaudeHome: model.ClaudeHome, ModelEndpoint: strings.TrimRight(model.BaseURL, "/") + "/chat/completions", APIKeyEnv: model.APIKeyEnv, MaxOutputTokens: model.MaxTokens, Admit: admit, TokenBudget: m.TokenBudget})
	if err != nil {
		return nil, fmt.Errorf("prepare project worker: %w", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = b.Close()
		return nil, errors.New("cannot open private local worker connection")
	}
	c, err := worker.New(worker.Config{Endpoint: "http://" + listener.Addr().String(), AuthToken: token, Capabilities: []string{"implement", "review"}})
	if err != nil {
		_ = listener.Close()
		_ = b.Close()
		return nil, err
	}
	runCtx, runCancel := context.WithCancel(m.ctx)
	server := &http.Server{Handler: b.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	r := &running{client: c, workspace: canonical, directory: dir, model: model, cancel: runCancel, server: server, done: make(chan struct{}), broker: b}
	m.running[projectID] = r
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.Diagnostics.Failure(diagnostics.Event{Component: "worker", Stage: "broker_http", ProjectID: projectID}, err)
			runCancel()
		}
	}()
	go func() {
		r.err = b.Run(runCtx)
		m.Diagnostics.Failure(diagnostics.Event{Component: "worker", Stage: "broker_runtime", ProjectID: projectID}, r.err)
		close(r.done)
	}()
	return c, nil
}
func saveManifest(dir string, m manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".environment-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "environment.json"))
}

// Close first cancels setup and model calls, waits for broker container cleanup,
// then releases durable-state locks. Virtual machines and cached images remain
// available for the next daemon; no unrelated runtime is stopped.
func (m *Manager) Close() error {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var result error
	for _, r := range m.running {
		_ = r.server.Close()
		r.cancel()
	}
	for _, r := range m.running {
		<-r.done
		result = errors.Join(result, r.err, r.broker.Close())
	}
	return result
}
