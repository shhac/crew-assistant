package workerbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shhac/crew-assistant/internal/diagnostics"
	"github.com/shhac/crew-assistant/internal/engine"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/integrations/worker"
)

type Broker struct {
	cfg            Config
	mu             sync.Mutex
	state          diskState
	lock           *flock.Flock
	wake           chan struct{}
	active         map[string]context.CancelFunc
	runCtx         context.Context
	started        bool
	quiesced       bool
	persistenceErr error
}

var pinned = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|[^\s]+@sha256:[a-f0-9]{64})$`)

func New(cfg Config) (*Broker, error) {
	if cfg.ProjectID == "" || cfg.StateDir == "" || cfg.Workspace == "" || cfg.Model == "" {
		return nil, errors.New("worker broker requires state directory, dedicated workspace, project ID, model and endpoint")
	}
	if !pinned.MatchString(cfg.Image) {
		return nil, errors.New("worker image must be pinned as sha256:<digest> or repository@sha256:<digest>")
	}
	if _, err := engine.New(engine.Config{Engine: cfg.Engine, Effort: cfg.Effort, CodexBin: cfg.CodexBin, Endpoint: cfg.ModelEndpoint, Model: cfg.Model}, engine.ExecutorFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("worker initialization does not execute tools")
	})); err != nil {
		return nil, err
	}
	if cfg.Engine != "codex" && cfg.Engine != "claude" && cfg.APIKeyEnv != "" && os.Getenv(cfg.APIKeyEnv) == "" {
		return nil, errors.New("worker model credential environment variable is not set")
	}
	if err := validateDependencies(cfg.Dependencies); err != nil {
		return nil, err
	}
	var err error
	if cfg.AuthToken == "" && (cfg.TokenEnv == "" || os.Getenv(cfg.TokenEnv) == "") {
		return nil, errors.New("worker broker authentication token environment variable is required")
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.MaxOutputTokens < 128 || cfg.MaxOutputTokens > 32768 || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 8 {
		return nil, errors.New("invalid worker model or concurrency bounds")
	}
	cfg.StateDir, err = filepath.Abs(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	cfg.Workspace, err = filepath.EvalSymlinks(cfg.Workspace)
	if err != nil {
		return nil, errors.New("worker workspace does not exist")
	}
	cfg.Workspace, err = filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(cfg.StateDir, ",\n\r") {
		return nil, errors.New("worker state path cannot contain commas or newlines")
	}
	if info, statErr := os.Stat(cfg.Workspace); statErr != nil || !info.IsDir() {
		return nil, errors.New("worker workspace must be an existing directory")
	}
	if err = os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	cfg.StateDir, err = filepath.EvalSymlinks(cfg.StateDir)
	if err != nil {
		return nil, errors.New("cannot resolve worker state directory")
	}
	if strings.ContainsAny(cfg.StateDir, ",\n\r") {
		return nil, errors.New("resolved state path cannot contain commas or newlines")
	}
	if inside(cfg.Workspace, cfg.StateDir) || inside(cfg.StateDir, cfg.Workspace) {
		return nil, errors.New("worker state and source workspace must be separate, non-nested directories")
	}

	lock := flock.New(filepath.Join(cfg.StateDir, "broker.lock"))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		return nil, errors.New("another worker broker owns this state directory")
	}
	fail := func(err error) (*Broker, error) { _ = lock.Unlock(); return nil, err }
	if cfg.Command == nil {
		cfg.Command, err = newDocker(cfg.DockerSocket, filepath.Join(cfg.StateDir, "docker-config"))
		if err != nil {
			return fail(err)
		}
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	imageID, err := cfg.Command.Run(checkCtx, []string{"image", "inspect", "--format", "{{.Id}}", cfg.Image}, nil)
	if err != nil || !regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(strings.TrimSpace(string(imageID))) {
		return fail(errors.New("pinned image must already exist in the local Docker daemon; the broker never pulls images"))
	}
	cfg.Image = strings.TrimSpace(string(imageID))
	b := &Broker{cfg: cfg, lock: lock, wake: make(chan struct{}, 1), active: map[string]context.CancelFunc{}, state: diskState{Runs: map[string]*storedRun{}, Receipts: map[string]receipt{}}}
	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "broker.json"))
	if err == nil {
		if json.Unmarshal(raw, &b.state) != nil || b.state.Runs == nil || b.state.Receipts == nil {
			return fail(errors.New("worker state is malformed; preserve it for recovery"))
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	// A prior process may have died while a command was executing. Stop only
	// containers recorded in our private state before declaring interruption.
	for _, r := range b.state.Runs {
		if r == nil {
			return fail(errors.New("worker state contains a missing run"))
		}
		r.Run.ControlCapabilities = []string{"pause", "resume", "stop"}
		if r.Request.ProjectID != cfg.ProjectID {
			return fail(errors.New("broker state belongs to another configured project"))
		}
		reconcileUsage(r, b.tokenBudget())
		reconcileDelivery(r)
		if r.Run.Status == "running" || r.Run.Status == "queued" {
			if r.Container != "" {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
				removeErr := reconcileContainer(cleanupCtx, cfg.Command, *r)
				stop()
				if removeErr != nil {
					return fail(errors.New("cannot reconcile prior worker container; inspect it before restarting the broker"))
				}
				// Confirmed gone, so nothing from the previous attempt can still be
				// writing. What those commands did stays unestablished in the
				// evidence; what they are doing now is nothing.
				r.UnsettledCommands = 0
			}
			if r.PendingStatus == "cancelled" {
				r.Run.Status = "cancelled"
				r.Run.Summary = "Cancellation completed during restart reconciliation"
				r.PendingStatus = ""
				r.PendingSummary = ""
			} else if r.PendingStatus == "paused" {
				r.Run.Status = "paused"
				r.Run.PauseRequested = false
				r.Run.Summary = "Owner-requested pause confirmed during restart cleanup"
				if r.PendingMessage != nil {
					r.Run.Message = r.PendingMessage
					r.PendingMessage = nil
				}
				r.PendingStatus = ""
				r.PendingSummary = ""
			} else if r.PendingStatus == "retry_wait" || r.PendingStatus == "usage_wait" {
				// A resource wait is still a resource wait after cleanup. Restart
				// is not extra allowance, and it is not a failed attempt either.
				r.Run.Status = r.PendingStatus
				r.Run.Summary = r.PendingSummary
				r.PendingStatus, r.PendingSummary = "", ""
			} else if r.PendingStatus == "blocked" {
				// A model/auth/budget blocker or prepared decision is still a
				// blocker after cleanup. Restart is not permission to retry it.
				r.Run.Status = "blocked"
				r.Run.Summary = r.PendingSummary
				r.PendingStatus, r.PendingSummary = "", ""
			} else if r.PendingMessage != nil && r.PendingStatus == "waiting" {
				// Cleanup is confirmed. Publish the durable outbox before allowing any
				// further model turn; a crash must not silently discard its request.
				r.Run.Message = r.PendingMessage
				r.PendingMessage = nil
				r.Run.Status = "waiting"
				r.Run.Summary = "Waiting for the daemon to route a peer message"
				r.PendingStatus = ""
				r.PendingSummary = ""
			} else {
				r.Run.Status = "interrupted"
				r.Run.Summary = "Broker restarted; previous session stopped. Review recorded artifacts before resuming."
			}
			if r.Run.Status == "paused" || r.Run.Status == "cancelled" {
				r.Run.PauseRequested = false
				r.Run.StopRequested = false
			}
			r.Run.UpdatedAt = now()
		}
	}
	if err = b.saveLocked(); err != nil {
		return fail(err)
	}
	return b, nil
}
func inside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func uid() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
func (b *Broker) saveLocked() (saveErr error) {
	defer func() {
		if saveErr != nil {
			b.cfg.Diagnostics.Failure(diagnostics.Event{Component: "worker", Stage: "state_persistence", ProjectID: b.cfg.ProjectID}, saveErr)
		}
	}()
	if b.persistenceErr != nil {
		return errors.New("worker persistence is unavailable; restart before accepting new work")
	}
	raw, err := json.Marshal(b.state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(b.cfg.StateDir, ".broker-*")
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
	if err = os.Rename(f.Name(), filepath.Join(b.cfg.StateDir, "broker.json")); err != nil {
		return err
	}
	dir, err := os.Open(b.cfg.StateDir)
	if err == nil {
		err = dir.Sync()
		_ = dir.Close()
	}
	if err != nil {
		b.persistenceErr = &committedSaveError{err}
		return b.persistenceErr
	}
	return nil
}
func (b *Broker) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
func (b *Broker) Run(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errors.New("worker broker runtime already started")
	}
	b.started = true
	b.runCtx = ctx
	b.mu.Unlock()
	var wg sync.WaitGroup
	defer func() {
		b.mu.Lock()
		for _, cancel := range b.active {
			cancel()
		}
		b.mu.Unlock()
		wg.Wait()
	}()
	b.signal()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.wake:
			for {
				b.mu.Lock()
				if b.persistenceErr != nil {
					err := b.persistenceErr
					b.mu.Unlock()
					return err
				}
				if len(b.active) >= b.cfg.MaxConcurrent {
					b.mu.Unlock()
					break
				}
				var selected string
				for id, r := range b.state.Runs {
					if _, active := b.active[id]; r.Run.Status == "queued" && !active {
						selected = id
						break
					}
				}
				if selected == "" {
					b.mu.Unlock()
					break
				}
				r := b.state.Runs[selected]
				r.PendingStatus = ""
				r.PendingSummary = ""
				r.Run.Status = "running"
				r.Run.Summary = "Preparing an isolated, offline workspace"
				r.Run.UpdatedAt = now()
				if err := b.saveLocked(); err != nil {
					b.mu.Unlock()
					return err
				}
				workCtx, cancel := context.WithCancel(ctx)
				b.active[selected] = cancel
				b.mu.Unlock()
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					b.execute(workCtx, id)
					b.mu.Lock()
					if current := b.state.Runs[id]; current == nil || current.Run.Status != "running" {
						delete(b.active, id)
					}
					// A still-running record after execute returns means cleanup or
					// persistence is unresolved. Retain its slot until restart reconciliation.
					b.mu.Unlock()
					cancel()
					b.signal()
				}(selected)
			}
		}
	}
}

// Close is called after Run returns, so no work can use state after lock release.
func (b *Broker) Close() error { return b.lock.Unlock() }
func (b *Broker) snapshot(id string) (storedRun, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.state.Runs[id]
	if r == nil {
		return storedRun{}, worker.ErrNotFound
	}
	raw, _ := json.Marshal(r)
	var copy storedRun
	_ = json.Unmarshal(raw, &copy)
	return copy, nil
}
func (b *Broker) update(id string, fn func(*storedRun) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.state.Runs[id]
	if r == nil {
		return worker.ErrNotFound
	}
	before, _ := json.Marshal(r)
	if err := fn(r); err != nil {
		var restored storedRun
		_ = json.Unmarshal(before, &restored)
		*r = restored
		return err
	}
	if err := b.saveLocked(); err != nil {
		if !saveCommitted(err) {
			var restored storedRun
			_ = json.Unmarshal(before, &restored)
			*r = restored
		}
		return err
	}
	return nil
}
func (b *Broker) Info() map[string]any {
	return map[string]any{"engine": b.cfg.Engine, "model": b.cfg.Model, "effort": b.cfg.Effort, "project_id": b.cfg.ProjectID, "image": b.cfg.Image, "workspace": b.cfg.Workspace, "capabilities": []string{"implement", "review"}, "manager": false, "network": "none"}
}
func (b *Broker) terminal(id, status, summary string) {
	_ = b.update(id, func(r *storedRun) error {
		if r.Run.Status == "cancelled" || r.PendingStatus == "cancelled" || r.PendingStatus == "paused" {
			return nil
		}
		r.PendingStatus = status
		r.PendingSummary = summary
		r.Run.Summary = "Finalizing isolated execution and collecting evidence"
		r.Run.UpdatedAt = now()
		return nil
	})
}
func (b *Broker) finalize(id, status, summary string, evidence []string) {
	_ = b.update(id, func(r *storedRun) error {
		if r.PendingStatus == "cancelled" || r.PendingStatus == "paused" {
			status = r.PendingStatus
			summary = r.PendingSummary
		}
		if r.Run.Status != "cancelled" {
			r.Run.Status = status
			r.Run.Summary = summary
		}
		if (status == "waiting" || status == "interrupted" || status == "paused") && r.PendingMessage != nil {
			r.Run.Message = r.PendingMessage
			r.PendingMessage = nil
		}
		r.Run.PauseRequested = false
		r.Run.StopRequested = false
		r.Run.Evidence = evidence
		r.Run.UpdatedAt = now()
		r.PendingStatus = ""
		r.PendingSummary = ""
		return nil
	})
}
func (b *Broker) progress(id, summary string) error {
	return b.update(id, func(r *storedRun) error {
		if r.Run.Status != "running" {
			return errInterrupted
		}
		r.Run.Summary = summary
		r.Run.UpdatedAt = now()
		return nil
	})
}
func (b *Broker) artifactEvidence(id string) []string {
	return []string{fmt.Sprintf("Worker artifacts: %s", filepath.Join(b.cfg.StateDir, "runs", id, "artifacts"))}
}

// committedSaveError means rename completed, but directory durability was not
// confirmed. Keep memory aligned with the visible file and stop new execution.
type committedSaveError struct{ err error }

func (e *committedSaveError) Error() string {
	return "worker state durability could not be confirmed; restart and reconcile before further work"
}
func (e *committedSaveError) Unwrap() error { return e.err }
func saveCommitted(err error) bool {
	var committed *committedSaveError
	return errors.As(err, &committed)
}
func reconcileContainer(ctx context.Context, command Commander, r storedRun) error {
	if r.Container != "agent-assistant-"+r.Run.ID {
		return errors.New("recorded container identity is invalid")
	}
	data, err := command.Run(ctx, []string{"container", "inspect", "--format", `{{.Id}} {{index .Config.Labels "agent-assistant.worker"}}`, r.Container}, nil)
	if err != nil {
		// A failed inspect is ambiguous. A successful exact-name listing that is
		// empty proves absence; daemon failures or an existing name remain unknown.
		names, listErr := command.Run(ctx, []string{"container", "ls", "--all", "--filter", "name=^/" + regexp.QuoteMeta(r.Container) + "$", "--format", "{{.Names}}"}, nil)
		if listErr == nil && strings.TrimSpace(string(names)) == "" {
			return nil
		}
		return errors.New("prior container identity could not be reconciled")
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[1] != r.Run.ID || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(fields[0]) {
		return errors.New("prior container ownership could not be verified")
	}
	_, err = command.Run(ctx, []string{"rm", "--force", fields[0]}, nil)
	return err
}

// settleContainerProcesses records that the workspace has stopped moving.
//
// A command whose client was cancelled left the question of whether it was still
// running open, and that question held back every later write. Confirmed removal
// of the container answers it: its processes are gone, whatever they were doing.
// What they did remains uncertain and stays in the evidence as such — the
// command records keep saying their outcome was never established — but the
// workspace itself is now still, which is what the next attempt needs.
func (b *Broker) settleContainerProcesses(id string) {
	settled := 0
	_ = b.update(id, func(r *storedRun) error {
		settled = r.UnsettledCommands
		if settled == 0 {
			return nil
		}
		r.UnsettledCommands = 0
		// Whatever those commands wrote is still unknown, and the next attempt has
		// to establish it rather than inherit the assumption that it is fine.
		r.Messages = append(r.Messages, fmt.Sprintf("Before continuing: %d command(s) in the previous attempt passed their time limit and were stopped without their outcome being established. The container has since been removed, so nothing is still writing, but what those commands left behind is unverified. Re-read the files they could have touched and re-run the checks they were meant to perform before relying on either.", settled))
		return nil
	})
	if settled > 0 {
		b.recordActivity(id, activityEntry{Kind: "recovery", Status: "settled", Detail: fmt.Sprintf("the container was confirmed removed, so %d command(s) of unknown outcome can no longer be changing the workspace; their results remain recorded as unestablished", settled)})
	}
}

// cleanupUncertain keeps the execution reservation because the container may
// still be alive. Restart reconciliation must confirm removal before releasing it.
func (b *Broker) cleanupUncertain(id, summary string) {
	_ = b.update(id, func(r *storedRun) error {
		r.Run.Status = "running"
		r.Run.Summary = summary
		r.Run.UpdatedAt = now()
		return nil
	})
}
