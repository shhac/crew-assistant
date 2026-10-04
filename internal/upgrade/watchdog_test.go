package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type watchdogClock struct{ at time.Time }

func (c watchdogClock) Now() time.Time                 { return c.at }
func (c watchdogClock) NewTimer(d time.Duration) Timer { return SystemClock().NewTimer(d) }

func TestWatchdogCrashAndHangRecoverTerminalAndLaunchd(t *testing.T) {
	for _, label := range []string{"", "fixture.launchd"} {
		for _, hang := range []bool{false, true} {
			t.Run(label+"/"+map[bool]string{false: "crash", true: "hang"}[hang], func(t *testing.T) {
				e, r, _ := engineFixture(t)
				dir := filepath.Dir(e.Path)
				state, config := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json")
				r.Step, r.LaunchdLabel, r.PID = Probation, label, 42
				r.ProcessIdentity = "fixture"
				r.StartedAt = time.Unix(100, 0)
				r.Deadline = time.Unix(101, 0)
				r.DetachedLog = filepath.Join(dir, "rollback-serve.log")
				r.Backups = Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
				fixtureFile(t, r.Backups.State, "old state")
				fixtureFile(t, r.Backups.Config, "old config")
				fixtureFile(t, state, "new state")
				fixtureFile(t, config, "new config")
				fixtureFile(t, state+"-wal", "new pages")
				fixtureFile(t, state+"-shm", "new index")
				if err := WriteRecord(e.Path, r); err != nil {
					t.Fatal(err)
				}
				alive := hang
				calls := []string{}
				w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Clock: watchdogClock{time.Unix(102, 0)}, Identity: func(int) string { return "fixture" }, Alive: func(int) bool { return alive }, Kill: func(int) error { calls = append(calls, "kill"); alive = false; return nil }}
				w.Acquire = func() (func(), bool, error) {
					calls = append(calls, "lock")
					var once sync.Once
					return func() { once.Do(func() { calls = append(calls, "unlock") }) }, true, nil
				}
				w.Restore = func(r Record) error {
					if alive {
						t.Fatal("restore while process alive")
					}
					got, _ := ReadRecord(e.Path)
					if got.Step != RollingBack {
						t.Fatal(got)
					}
					calls = append(calls, "restore")
					return Restore(state, config, r.Backups)
				}
				start := func(kind string) func(Record) error {
					return func(r Record) error {
						got, _ := ReadRecord(e.Path)
						if got.Step != RolledBack || !got.Pinned || got.Failure == "" {
							t.Fatal(got)
						}
						calls = append(calls, kind)
						return nil
					}
				}
				w.StartDetached, w.Kickstart = start("detached"), start("kickstart")
				done, err := w.Tick()
				if err != nil || done {
					t.Fatal(done, err)
				}
				want := []string{"lock", "restore", "unlock", "detached"}
				if label != "" {
					want[3] = "kickstart"
				}
				if hang {
					want = append([]string{"kill"}, want...)
				}
				if !reflect.DeepEqual(calls, want) {
					t.Fatal(calls)
				}
				for path, body := range map[string]string{state: "old state", config: "old config"} {
					b, _ := os.ReadFile(path)
					if string(b) != body {
						t.Fatal(path, string(b))
					}
				}
				for _, suffix := range []string{"-wal", "-shm"} {
					if _, err = os.Stat(state + suffix); !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
				}
				result, err := e.Start(r.From, r.SavedBinary)
				if err != nil || !result.Failed {
					t.Fatal(result, err)
				}
			})
		}
	}
}

func TestWatchdogStandsDownAndDoesNotRestoreWhenLockOwned(t *testing.T) {
	for _, step := range []Step{Healthy, RolledBack, StoppedInProbation, InstallFailed, Abandoned} {
		e, r, _ := engineFixture(t)
		r.Step = step
		if err := WriteRecord(e.Path, r); err != nil {
			t.Fatal(err)
		}
		w := Watchdog{Path: e.Path, Alive: func(int) bool { t.Fatal("terminal step examined PID"); return false }}
		if done, err := w.Tick(); err != nil || !done {
			t.Fatal(step, done, err)
		}
	}
	e, r, _ := engineFixture(t)
	r.Step = HandingOver
	r.StartedAt = time.Now()
	if err := WriteRecord(e.Path, r); err != nil {
		t.Fatal(err)
	}
	w := Watchdog{Path: e.Path, Attempt: r.StartedAt, Alive: func(int) bool { return false }, Acquire: func() (func(), bool, error) { return nil, false, nil }, Restore: func(Record) error { t.Fatal("restored without lock"); return nil }}
	if done, err := w.Tick(); err != nil || done {
		t.Fatal(done, err)
	}
}
