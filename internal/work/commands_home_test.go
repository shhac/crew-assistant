//go:build !windows

package work

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shhac/lib-agent-harness/sandbox"
)

// A hosted command's own tests can make a socket in its scratch: the harness
// adds its layout below the home, and a socket path holds about a hundred bytes.
func TestTheCommandsHomeLeavesRoomForASocket(t *testing.T) {
	t.Parallel()
	lp := testLoop(t)
	lp.tmpRoot = ""
	home := lp.commandsHome()
	if !strings.HasPrefix(home, "/tmp/") && !strings.HasPrefix(home, "/private/tmp/") {
		t.Fatalf("home %s", home)
	}
	deepest := filepath.Join(home, "commands", "sandbox-1234567890", "workbench", "tmp", "c-1234567890", "owner.sock")
	if len(deepest) > 100 {
		t.Fatalf("a socket in a command's scratch takes %d bytes: %s", len(deepest), deepest)
	}
	other := testLoop(t)
	other.tmpRoot = ""
	if other.commandsHome() == home {
		t.Fatal("two states share one home")
	}
}

// Under a shared /tmp, a home someone else made, or anyone else can open, is
// refused rather than used.
func TestACommandsHomeMustBePrivate(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ok := filepath.Join(root, "ok")
	if err := privateHome(ok); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(ok); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("made %v %v", st, err)
	}
	if err := privateHome(ok); err != nil {
		t.Fatal("an existing private home was refused:", err)
	}
	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if privateHome(open) == nil {
		t.Fatal("a home anyone can open was used")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(ok, link); err != nil {
		t.Fatal(err)
	}
	if privateHome(link) == nil {
		t.Fatal("a link was used as a home")
	}
}

// What a version up to v0.68.0 left beside the state is still swept.
func TestASweepReclaimsTheHomeEarlierVersionsUsed(t *testing.T) {
	t.Parallel()
	lp := testLoop(t)
	legacy := filepath.Join(lp.Core.StateDirectory(), "commands")
	stale := filepath.Join(legacy, "commands", "left-by-v0.68")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	var homes []string
	lp.commands = func(_ context.Context, o sandbox.Options) (commandSandbox, error) {
		homes = append(homes, o.RuntimeHome)
		return &fakeCommands{onClose: func() { os.RemoveAll(stale) }}, nil
	}
	if err := lp.sweepCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(homes) != 1 || homes[0] != legacy {
		t.Fatalf("swept %v", homes)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("the earlier home's leftover is still there", err)
	}
}

// Inside another sandbox only its own scratch can be written: when /tmp
// refuses the home, a real sandbox opens in the system's temporary directory
// instead, and keeps to it.
func TestTheCommandsHomeMovesWhenTmpIsRefused(t *testing.T) {
	t.Parallel()
	lp := testLoop(t)
	locked, err := os.MkdirTemp("", "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700); os.RemoveAll(locked) })
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	lp.tmpRoot = locked
	home, err := lp.makeCommandsHome()
	if err != nil {
		t.Fatal(err)
	}
	if home != lp.tempHome() || lp.commandsHome() != home || !strings.HasPrefix(home, os.TempDir()) {
		t.Fatalf("home %s, commandsHome %s", home, lp.commandsHome())
	}
	if st, err := os.Stat(home); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("home %v %v", st, err)
	}
	if !slices.Contains(lp.commandsHomes(), lp.tmpHome()) || !slices.Contains(lp.commandsHomes(), home) {
		t.Fatalf("a sweep would miss a home: %v", lp.commandsHomes())
	}
}
