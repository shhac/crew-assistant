package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/upgrade"
	libcli "github.com/shhac/lib-agent-cli/cli"
	"github.com/spf13/cobra"
)

func TestUpgradeStatusReadsRecordWithoutDaemon(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-record", true: "rollback"}[pinned], func(t *testing.T) {
			o := &options{statePath: filepath.Join(t.TempDir(), "state.db"), globals: &libcli.Globals{Format: "ndjson"}}
			if pinned {
				r := upgrade.Record{Step: upgrade.RolledBack, From: "v1.0.0", To: "v2.0.0", Failure: "API health failed", Pinned: true, DetachedLog: "/fixture/rollback.log"}
				if err := upgrade.WriteRecord(upgrade.RecordPath(o.statePath), r); err != nil {
					t.Fatal(err)
				}
			}
			root := &cobra.Command{Use: "test"}
			registerUpgrade(root, o)
			root.SetArgs([]string{"upgrade", "status"})
			out, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			original := os.Stdout
			os.Stdout = out
			err = root.Execute()
			os.Stdout = original
			if err != nil {
				t.Fatal(err)
			}
			if _, err = out.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err = json.NewDecoder(out).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got["rollback_in_force"] != pinned {
				t.Fatal(got)
			}
			if pinned && (got["status"] != "Rollback is in force" || got["clear"] != "crew-assistant upgrade clear-rollback" || got["log"] != "/fixture/rollback.log") {
				t.Fatal(got)
			}
		})
	}
}

func TestClearRollbackWithoutDaemonRefusesPlainly(t *testing.T) {
	o := &options{statePath: filepath.Join(t.TempDir(), "state.db"), globals: &libcli.Globals{Format: "ndjson"}}
	root := &cobra.Command{Use: "test"}
	registerUpgrade(root, o)
	root.SetArgs([]string{"upgrade", "clear-rollback"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "daemon is not running") {
		t.Fatal(err)
	}
}

func TestAbandonedCleanupAndHealthyPruningKeepCurrentBackups(t *testing.T) {
	path := upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db"))
	root := filepath.Join(filepath.Dir(path), "attempts")
	for _, attempt := range []string{"older", "current"} {
		if err := os.MkdirAll(filepath.Join(root, attempt), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, attempt, "state.db"), []byte(attempt), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := upgrade.Record{SavedBinary: filepath.Join(root, "current", "previous")}
	if err := cleanupUpgradeAttempts(path, r, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "older")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "current", "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := cleanupUpgradeAttempts(path, r, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
