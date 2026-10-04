package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/server"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

const upgradeHealthBound = 2 * time.Minute

type upgradeHost struct {
	engine             *upgrade.Engine
	lock               *flock.Flock
	start              upgrade.StartResult
	executable, prefix string
	listenerFile       *os.File
	checker            *upgrade.Checker
	startDetached      func(string, []string, string) error
	probe              func(context.Context, string, string) error
}

func prepareUpgradeStart(o *options, stop lifecycle.Stop, demo bool) (_ *upgradeHost, resultErr error) {
	if err := canonicalUpgradeOptions(o); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(o.statePath), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(o.statePath + ".lock")
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		r, _ := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
		if r != nil && r.Pinned && currentUpgradeLaunchdLabel() == "" {
			version := r.To
			if r.PinTo != "" {
				version = r.PinTo
			}
			return nil, fmt.Errorf("another daemon owns this state file. Rollback is in force after %s failed: %s. Detached daemon log: %s. Clear with crew-assistant upgrade clear-rollback", version, r.Failure, r.DetachedLog)
		}
		return nil, errors.New("another daemon owns this state file")
	}
	protectInheritedUpgradeListener()
	h := &upgradeHost{lock: lock, startDetached: o.upgradeStarter}
	if demo {
		r, err := upgrade.ReadRecord(upgrade.RecordPath(o.statePath))
		if err != nil || r != nil {
			_ = lock.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, errors.New("demo cannot open state with an upgrade record; use a fresh --state file")
		}
		return h, nil
	}
	defer func() {
		if resultErr != nil {
			_ = lock.Unlock()
		}
	}()
	executable := o.upgradeExecutable
	if executable == nil {
		executable = os.Executable
	}
	h.executable, err = executable()
	if err != nil {
		return nil, err
	}
	h.executable, err = filepath.EvalSymlinks(h.executable)
	if err != nil {
		return nil, err
	}
	h.prefix, _ = upgrade.HomebrewPrefix(h.executable)
	h.engine = &upgrade.Engine{Path: upgrade.RecordPath(o.statePath), Drain: func() bool { return stop.Drain("upgrade") }, Close: func() error { return nil }, Exec: func(binary string, _ []string) error {
		if o.upgradeExecer != nil {
			return o.upgradeExecer(binary, upgradeProcessArgs(o, os.Args[1:]))
		}
		return replaceUpgradeProcess(binary, upgradeProcessArgs(o, os.Args[1:]))
	}}
	h.engine.Report = func(message string) { fmt.Fprintln(os.Stderr, message) }
	h.engine.Arm = func(r upgrade.Record) error { return h.arm(o, r) }
	h.engine.Restore = func(r upgrade.Record) error {
		if err := validateUpgradeConfig(r, o.configPath); err != nil {
			return err
		}
		return upgrade.Restore(o.statePath, o.configPath, r.Backups)
	}
	h.engine.Interrupted = func() bool { return stop.Reason() == "signal" }
	h.engine.Cleanup = func(r upgrade.Record, healthy bool) error { return cleanupUpgradeAttempts(h.engine.Path, r, healthy) }
	existing, readErr := upgrade.ReadRecord(h.engine.Path)
	if readErr != nil {
		return nil, readErr
	}
	if existing != nil {
		if err = validateUpgradeStartConfig(*existing, o.configPath, o.version); err != nil {
			return nil, err
		}
	}
	h.start, err = h.engine.Start(o.version, h.executable)
	if err != nil {
		return nil, err
	}
	r, readErr := upgrade.ReadRecord(h.engine.Path)
	if readErr != nil {
		err = readErr
		return nil, err
	}
	if r != nil && (r.Pinned || filepath.Clean(h.executable) == filepath.Clean(r.SavedBinary) || (r.Step == upgrade.InstallFailed || r.Step == upgrade.BackupFailed) && filepath.Clean(h.executable) == filepath.Clean(r.RunningBinary)) {
		h.prefix = r.Prefix
	}
	if (h.start.Probation || h.start.Recovery) && r != nil {
		if h.start.Recovery {
			err = h.engine.BeginRecovery(os.Getpid(), confirmedUpgradeIdentity(o), upgradeProcessArgs(o, os.Args[1:]), upgradeHealthBound)
		} else {
			err = h.engine.BeginProbation(os.Getpid(), confirmedUpgradeIdentity(o), upgradeProcessArgs(o, os.Args[1:]), upgradeHealthBound)
		}
		if err != nil {
			if stop.Reason() == "signal" {
				err = errors.Join(err, h.engine.StopProbation())
			}
			return nil, err
		}
		r, err = upgrade.ReadRecord(h.engine.Path)
		if err != nil {
			return nil, err
		}
		go func() {
			<-stop.Graceful.Done()
			if stop.Reason() == "signal" {
				if err := h.engine.StopProbation(); err != nil {
					h.engine.Report("Could not record the owner stop; upgrade recovery may still restart the daemon. Check access to the upgrade record.")
				}
			}
		}()
		if err = h.arm(o, *r); err != nil {
			if h.start.Recovery {
				h.engine.Report("Rollback watchdog could not start; completing direct recovery. Restart remains pending until the API answers.")
			} else {
				err = h.engine.Fail("could not start the upgrade watchdog")
				return nil, err
			}
		}

	}
	return h, nil
}

// A first signal finishes the installer. Only Force cancels admitted work.
func upgradeInstallContext(stop lifecycle.Stop) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-ctx.Done():
		case <-stop.Force.Done():
			cancel()
		}
	}()
	return ctx, cancel
}

// GUI terminal applications also set XPC_SERVICE_NAME.
func upgradeLaunchdLabel(label string, parent int) string {
	if parent != 1 || label == "0" || strings.HasPrefix(label, "application.") {
		return ""
	}
	return label
}
func currentUpgradeLaunchdLabel() string {
	return upgradeLaunchdLabel(os.Getenv("XPC_SERVICE_NAME"), os.Getppid())
}

func cleanupUpgradeAttempts(recordPath string, r upgrade.Record, healthy bool) error {
	recordPath, err := filepath.Abs(recordPath)
	if err != nil {
		return err
	}
	root := filepath.Join(filepath.Dir(recordPath), "attempts")
	current, err := filepath.Abs(filepath.Dir(r.SavedBinary))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if healthy && path != current || !healthy && path == current {
			if err = os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *upgradeHost) request(o *options, address string, version string, automatic bool) error {
	if h.prefix == "" {
		return errors.New("this install cannot self-upgrade; upgrade by hand")
	}
	if !core.ValidVersion(version) || core.CompareVersions(version, o.version) <= 0 {
		return errors.New("no newer installable version is available")
	}
	now := time.Now().UTC()
	dir := filepath.Join(filepath.Dir(h.engine.Path), "attempts", strconv.FormatInt(now.UnixNano(), 10))
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	configPath, err := upgrade.CanonicalPath(o.configPath)
	if err != nil {
		return err
	}
	r := upgrade.Record{ConfigPath: configPath, RunningBinary: h.executable, From: o.version, To: version, Prefix: h.prefix, Automatic: automatic, PID: os.Getpid(), Address: address, LaunchdLabel: currentUpgradeLaunchdLabel(), Args: upgradeServeArgs(os.Args[1:]), SavedBinary: filepath.Join(dir, "crew-assistant-"+o.version), Backups: upgrade.Backups{State: filepath.Join(dir, "state.db"), Config: filepath.Join(dir, "config.json")}}
	r.From = "v" + strings.TrimPrefix(r.From, "v")
	r.ProcessIdentity = confirmedUpgradeIdentity(o)
	if r.ProcessIdentity == "" {
		return errors.New("cannot start an upgrade without a confirmed daemon process identity; retry after local process inspection works")
	}
	r.Args = upgradeProcessArgs(o, os.Args[1:])
	return h.engine.Request(r)
}

// Pin exec honors the current startup flags (including --no-dispatch), while
// watchdog restart uses these same arguments recorded for that process. Make
// file paths absolute so an owner's later terminal CWD cannot select new state.
func upgradeProcessArgs(o *options, args []string) []string {
	out := upgradeServeArgs(args)
	for i, arg := range out {
		if arg == "--env-file" && i+1 < len(out) {
			if path, err := filepath.Abs(out[i+1]); err == nil {
				out[i+1] = path
			}
		}
		if strings.HasPrefix(arg, "--env-file=") {
			if path, err := filepath.Abs(strings.TrimPrefix(arg, "--env-file=")); err == nil {
				out[i] = "--env-file=" + path
			}
		}
	}
	state, config := o.statePath, o.configPath
	if path, err := filepath.Abs(state); err == nil {
		state = path
	}
	if path, err := filepath.Abs(config); err == nil {
		config = path
	}
	return append(out, "--state", state, "--config", config)
}

func upgradeServeArgs(args []string) []string {
	out := []string{}
	for _, arg := range args {
		if arg != "--open" && !strings.HasPrefix(arg, "--open=") {
			out = append(out, arg)
		}
	}
	return out
}

// wire supplies effects only after the store, auth and listener exist. close
// drains HTTP before closing SQLite, and is safe to call again on rollback.
func (h *upgradeHost) wire(o *options, cfg func() config.Config, store *core.Store, auth *server.Auth, listener net.Listener, httpServer *http.Server) {
	var once sync.Once
	var closeErr error
	h.engine.Close = func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := httpServer.Shutdown(ctx)
			if err != nil {
				_ = httpServer.Close()
			}
			// Closing HTTP is required before restoring a database any request
			// may hold. Shutdown timeout refuses rollback rather than racing.
			closeErr = errors.Join(err, auth.SaveHandover(o.runtimeDir()), store.Close())
		})
		return closeErr
	}
	h.engine.Backup = func(ctx context.Context, r upgrade.Record) error {
		var err error
		h.listenerFile, err = preserveUpgradeListener(listener)
		if err != nil {
			return err
		}
		if err = os.Setenv(upgradeListenerEnv, upgradeListenerDescriptor(h.listenerFile)); err != nil {
			return err
		}
		if err := upgrade.EnsureDirectory(filepath.Dir(r.Backups.State)); err != nil {
			return err
		}
		if err := store.Backup(ctx, r.Backups.State); err != nil {
			return err
		}
		if _, err := os.Stat(o.configPath); errors.Is(err, os.ErrNotExist) {
			if err = config.Save(o.configPath, cfg()); err != nil {
				return err
			}
		}
		if err := upgrade.CopyFile(o.configPath, r.Backups.Config, 0600); err != nil {
			return err
		}
		if err := upgrade.CopyFile(h.executable, r.SavedBinary, 0700); err != nil {
			return err
		}
		return auth.SaveHandover(o.runtimeDir())
	}
	h.engine.Install = func(ctx context.Context, r upgrade.Record) (string, error) {
		return (upgrade.BrewInstaller{}).Install(ctx, r.Prefix, cfg().Upgrade.Formula, os.Environ())
	}
	h.engine.Handover = func(ctx context.Context, r upgrade.Record) error {
		r.Deadline = time.Now().Add(upgradeHealthBound)
		if err := upgrade.WriteRecord(h.engine.Path, r); err != nil {
			return err
		}
		// Recovery must already be running if HTTP shutdown or session persistence fails.
		if err := h.arm(o, r); err != nil {
			return err
		}
		return h.engine.Close()
	}

}

func probeUpgradeAPI(ctx context.Context, url, tokenPath string) error {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return errors.New("health check could not read its admin token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/state", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(token))
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("health check could not reach its API")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("health check returned HTTP %d", response.StatusCode)
	}
	return nil
}

func upgradeWatchdog(o *options, attempt time.Time) *upgrade.Watchdog {
	return &upgrade.Watchdog{Path: upgrade.RecordPath(o.statePath), Attempt: attempt, Report: func(message string) { fmt.Fprintln(os.Stderr, message) }, Identity: upgradeProcessIdentity, Alive: upgradePIDAlive, Kill: killUpgradePID,
		Acquire: func() (func(), bool, error) {
			lock := flock.New(o.statePath + ".lock")
			ok, err := lock.TryLock()
			var once sync.Once
			return func() { once.Do(func() { _ = lock.Unlock() }) }, ok, err
		},
		Restore: func(r upgrade.Record) error { return upgrade.Restore(o.statePath, o.configPath, r.Backups) },
		StartDetached: func(r upgrade.Record) error {
			r.DetachedLog = filepath.Join(filepath.Dir(upgrade.RecordPath(o.statePath)), "rollback-serve.log")
			return startUpgradeDetached(upgrade.RecoveryBinary(r), r.Args, r.DetachedLog)
		},
		Kickstart: func(r upgrade.Record) error {
			cmd := exec.Command("/bin/launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/"+r.LaunchdLabel)
			if err := cmd.Run(); err != nil {
				return errors.New("launchd could not restart the restored version; start crew-assistant serve")
			}
			return nil
		},
	}
}

func validateUpgradeStartConfig(r upgrade.Record, config, running string) error {
	// Only the previous version makes an ordinary start from a completed
	// failure. The target still needs the recorded destinations for probation.
	if !r.Pinned && !r.RestartPending && !r.RecoveryStarting && (r.Step == upgrade.BackupFailed || r.Step == upgrade.InstallFailed) && strings.TrimPrefix(running, "v") == strings.TrimPrefix(r.From, "v") {
		return nil
	}
	return validateUpgradeConfig(r, config)
}

func validateUpgradeConfig(r upgrade.Record, config string) error {
	if !r.Pinned && !r.RestartPending && (r.Step == upgrade.Healthy || r.Step == upgrade.Abandoned) {
		return nil
	}
	absolute, err := upgrade.CanonicalPath(config)
	if err != nil {
		return err
	}
	recorded := r.ConfigPath
	if recorded != "" {
		recorded, err = upgrade.CanonicalPath(recorded)
		if err != nil {
			return err
		}
	}
	if recorded != "" && recorded != absolute {
		return errors.New("upgrade record belongs to a different config file; start with the recorded --config path")
	}
	return nil
}

// The runner itself holds this lease for its lifetime; startup only launches
// another helper when the existing one has exited.
func startUpgradeWatchdogIfAbsent(path string, attempt time.Time, start func() error) error {
	lock := flock.New(upgrade.WatchdogLockPath(path, attempt))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	_ = lock.Unlock()
	return start()
}

func (h *upgradeHost) arm(o *options, r upgrade.Record) error {
	start := h.startDetached
	if start == nil {
		start = startUpgradeDetached
	}
	args := []string{"--state", o.statePath, "--config", o.configPath, "upgrade", "watch", "--pid", strconv.Itoa(r.PID), "--attempt", r.StartedAt.Format(time.RFC3339Nano)}
	return startUpgradeWatchdogIfAbsent(h.engine.Path, r.StartedAt, func() error {
		return start(upgrade.RecoveryBinary(r), args, filepath.Join(filepath.Dir(h.engine.Path), "watchdog.log"))
	})
}

func canonicalUpgradeOptions(o *options) error {
	var err error
	o.statePath, err = upgrade.CanonicalPath(o.statePath)
	if err != nil {
		return err
	}
	o.configPath, err = upgrade.CanonicalPath(o.configPath)
	return err
}

// A transient inspection failure must not publish an empty identity. Each
// production probe is bounded; after three failures admission is refused.
func confirmedUpgradeIdentity(o *options) string {
	inspect := o.upgradeIdentity
	if inspect == nil {
		inspect = upgradeProcessIdentity
	}
	for range 3 {
		if identity := inspect(os.Getpid()); identity != "" {
			return identity
		}
	}
	return ""
}
