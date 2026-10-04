package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDurableUpgradeRecord(t *testing.T) {
	path := RecordPath(filepath.Join(t.TempDir(), "state.db"))
	if got, err := ReadRecord(path); err != nil || got != nil {
		t.Fatalf("%+v %v", got, err)
	}
	r := Record{Step: RollingBack, From: "v1.0.0", To: "v2.0.0", Pinned: true}
	if err := WriteRecord(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecord(path)
	if err != nil || !reflect.DeepEqual(*got, r) {
		t.Fatalf("%+v %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions: %v %v", info, err)
	}
	r.Step = Step("future-step")
	if err := WriteRecord(path, r); err == nil {
		t.Fatal("accepted unknown recovery step")
	}
	fixtureFile(t, path, `{"step":"future-step"}`)
	if _, err := ReadRecord(path); err == nil {
		t.Fatal("unknown recovery step must fail closed")
	}
}

func TestRestoreRepeatableAndRemovesSQLiteCompanions(t *testing.T) {
	dir := t.TempDir()
	state, config := filepath.Join(dir, "state.db"), filepath.Join(dir, "config.json")
	backups := Backups{State: filepath.Join(dir, "old.db"), Config: filepath.Join(dir, "old.json")}
	fixtureFile(t, backups.State, "old state")
	fixtureFile(t, backups.Config, "old config")
	for range 2 {
		fixtureFile(t, state, "new state")
		fixtureFile(t, config, "new config")
		fixtureFile(t, state+"-wal", "new pages")
		fixtureFile(t, state+"-shm", "new index")
		if err := Restore(state, config, backups); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{state: "old state", config: "old config", backups.State: "old state", backups.Config: "old config"} {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("%s: %q %v", path, got, err)
			}
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(state + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("SQLite companion survived", err)
			}
		}
	}
	fixtureFile(t, state, "untouched")
	backups.Config = filepath.Join(dir, "missing")
	if err := Restore(state, config, backups); err == nil {
		t.Fatal("missing backup accepted")
	}
	got, _ := os.ReadFile(state)
	if string(got) != "untouched" {
		t.Fatal("state changed before all backups were readable")
	}
}

func TestInstallerUsesExecutablePrefixAndRemovesCleanupOverride(t *testing.T) {
	prefix := t.TempDir()
	executable := filepath.Join(prefix, "Cellar", "crew-assistant", "1.0.0", "bin", "crew-assistant")
	fixtureFile(t, executable, "binary")
	link := filepath.Join(prefix, "crew-assistant")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	got, err := HomebrewPrefix(link)
	resolvedPrefix, resolveErr := filepath.EvalSymlinks(prefix)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if err != nil || got != resolvedPrefix {
		t.Fatalf("prefix = %q, want %q: %v", got, resolvedPrefix, err)
	}
	if _, err := HomebrewPrefix(filepath.Join(prefix, "missing")); err == nil {
		t.Fatal("missing binary accepted")
	}
	standalone := filepath.Join(prefix, "standalone", "crew-assistant")
	fixtureFile(t, standalone, "binary")
	if _, err := HomebrewPrefix(standalone); err == nil {
		t.Fatal("standalone accepted")
	}
	calls := 0
	i := BrewInstaller{Run: func(_ context.Context, name string, args, env []string) ([]byte, error) {
		calls++
		for _, entry := range env {
			if strings.HasPrefix(entry, "HOMEBREW_NO_INSTALL_CLEANUP=") {
				t.Fatal("cleanup override survived")
			}
		}
		if !reflect.DeepEqual(env, []string{"PATH=/wrong/bin", "KEEP=yes"}) {
			t.Fatal(env)
		}
		if calls == 1 {
			if name != filepath.Join(resolvedPrefix, "bin", "brew") || !reflect.DeepEqual(args, []string{"upgrade", "fixture/tap/crew-assistant"}) {
				t.Fatal(name, args)
			}
			return nil, nil
		}
		if name != filepath.Join(resolvedPrefix, "opt", "crew-assistant", "bin", "crew-assistant") || !reflect.DeepEqual(args, []string{"--version"}) {
			t.Fatal(name, args)
		}
		return []byte("crew-assistant v2.0.0\n"), nil
	}}
	v, err := i.Install(context.Background(), got, "fixture/tap/crew-assistant", []string{"PATH=/wrong/bin", "HOMEBREW_NO_INSTALL_CLEANUP=1", "KEEP=yes"})
	if err != nil || v != "v2.0.0" || calls != 2 {
		t.Fatal(v, err, calls)
	}
}

func TestInstallerFailureDoesNotExposeOutput(t *testing.T) {
	calls := 0
	i := BrewInstaller{Run: func(context.Context, string, []string, []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("credential=secret"), errors.New("credential=secret")
		}
		return []byte("v2.0.0"), nil
	}}
	v, err := i.Install(context.Background(), "/fixture", "fixture/tap/crew-assistant", nil)
	if v != "v2.0.0" || err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(v, err)
	}
}

func TestInstallAndProbeFailuresBothReportedWithoutSecrets(t *testing.T) {
	i := BrewInstaller{Run: func(context.Context, string, []string, []string) ([]byte, error) {
		return []byte("credential=secret"), errors.New("credential=secret")
	}}
	_, err := i.Install(context.Background(), "/fixture", "fixture/tap/crew-assistant", nil)
	if err == nil {
		t.Fatal("failed install accepted")
	}
	for _, text := range []string{"Homebrew upgrade failed", "version could not be confirmed", "by hand"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatal(err)
		}
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestCopyFilePrivateAndPreservesDestinationOnFailure(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	fixtureFile(t, source, strings.Repeat("backup", 1024*1024))
	fixtureFile(t, destination, "existing")
	if err := CopyFile(source, destination, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0700 || info.Size() != 6*1024*1024 {
		t.Fatal(info, err)
	}
	if err := CopyFile(filepath.Join(dir, "missing"), destination, 0600); err == nil {
		t.Fatal("missing source accepted")
	}
	info, _ = os.Stat(destination)
	if info.Size() != 6*1024*1024 {
		t.Fatal("destination changed on failure")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".upgrade-copy-*"))
	if err != nil || len(matches) != 0 {
		t.Fatal("temporary copies leaked", matches, err)
	}
}

func TestInstallerWithFakeHomebrewAndBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	prefix := t.TempDir()
	brew := filepath.Join(prefix, "bin", "brew")
	binary := filepath.Join(prefix, "opt", "crew-assistant", "bin", "crew-assistant")
	fixtureFile(t, brew, "#!/bin/sh\n[ \"${HOMEBREW_NO_INSTALL_CLEANUP+x}\" != x ] || exit 7\n[ \"$1\" = upgrade ] || exit 8\n[ \"$2\" = fixture/tap/crew-assistant ] || exit 9\n[ \"$#\" = 2 ] || exit 10\n")
	fixtureFile(t, binary, "#!/bin/sh\n[ \"$1\" = --version ] || exit 11\necho 'crew-assistant v2.0.0'\n")
	for _, path := range []string{brew, binary} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	v, err := (BrewInstaller{}).Install(context.Background(), prefix, "fixture/tap/crew-assistant", []string{"PATH=/nonexistent", "HOMEBREW_NO_INSTALL_CLEANUP=1"})
	if err != nil || v != "v2.0.0" {
		t.Fatal(v, err)
	}
}
