package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shhac/crew-assistant/internal/config"
	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/sandbox"
)

func TestToolchainRootsDoNotGrantSharedHomeOrCrewState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "custom-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "custom-config"))
	paths, err := config.Paths()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{filepath.Join(home, ".local"), filepath.Join(home, ".config"), filepath.Dir(filepath.Dir(paths.State)), filepath.Dir(filepath.Dir(paths.Config)), filepath.Join(home, "nvm", "versions", "node", "v24")} {
		for _, sub := range []string{"bin", "include/node"} {
			if err := os.MkdirAll(filepath.Join(root, sub), 0700); err != nil {
				t.Fatal(err)
			}
		}
		binary := filepath.Join(root, "bin", "node")
		if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
		gotGo, gotNode := toolchainRoot(root), nodeInstallRoot(binary)
		if strings.Contains(root, "nvm") {
			if gotGo == "" || gotNode != gotGo {
				t.Fatalf("dedicated install not granted: %q %q", gotGo, gotNode)
			}
		} else if gotGo != "" || gotNode != "" {
			t.Fatalf("shared root %q granted: Go=%q Node=%q", root, gotGo, gotNode)
		}
	}
	// An alias cannot hide that the root contains state; state may not exist yet.
	alias := filepath.Join(home, "sdk-alias")
	if err := os.Symlink(filepath.Dir(filepath.Dir(paths.State)), alias); err != nil {
		t.Fatal(err)
	}
	if got := toolchainRoot(alias); got != "" {
		t.Fatalf("granted state alias: %q", got)
	}
	plain := filepath.Join(home, "plain-prefix", "bin")
	if err := os.MkdirAll(plain, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(plain, "node")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := nodeInstallRoot(binary); got != "" {
		t.Fatalf("unidentified Node prefix granted: %q", got)
	}
}

func TestReadableGrantsOnlyNamedToolchainRoots(t *testing.T) {
	oldGo, oldNode, oldCache := goRoot, nodeRoot, moduleCache
	t.Cleanup(func() { goRoot, nodeRoot, moduleCache = oldGo, oldNode, oldCache })
	root := t.TempDir()
	goDir, nodeDir := filepath.Join(root, "sdk", "go"), filepath.Join(root, "nvm", "node")
	for _, dir := range []string{goDir, nodeDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []string{"bin", "include/node"} {
		if err := os.MkdirAll(filepath.Join(nodeDir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	nodeBinary := filepath.Join(nodeDir, "bin", "node")
	if err := os.WriteFile(nodeBinary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	goRoot = func() string { return toolchainRoot(goDir) }
	nodeRoot = func() string { return nodeInstallRoot(nodeBinary) }
	moduleCache = func() string { return "" }
	canonicalGo, _ := filepath.EvalSymlinks(goDir)
	canonicalNode, _ := filepath.EvalSymlinks(nodeDir)
	if got := (Repo{}).Readable(); !slices.Equal(got, []string{canonicalGo, canonicalNode}) {
		t.Fatal(got)
	}
	home, _ := os.UserHomeDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"relative", filepath.Join(root, "missing"), file, home, filepath.Dir(home)} {
		if got := toolchainRoot(invalid); got != "" {
			t.Errorf("granted %q as %q", invalid, got)
		}
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	if got := toolchainRoot(filepath.Join(root, "broken")); got != "" {
		t.Fatal(got)
	}
}

// Exercise the library's execution policy with the same grants used by checks.
// Nested sandbox proof refusals are asserted, never skipped or weakened.
func TestGrantedToolchainCommandSandbox(t *testing.T) {
	oldGo, oldNode, oldCache := goRoot, nodeRoot, moduleCache
	t.Cleanup(func() { goRoot, nodeRoot, moduleCache = oldGo, oldNode, oldCache })
	toolchain := t.TempDir()
	bin := filepath.Join(toolchain, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "crew-fixture-go"), []byte("#!/bin/sh\nprintf 'granted-toolchain'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	goRoot = func() string { return toolchainRoot(toolchain) }
	nodeRoot, moduleCache = func() string { return "" }, func() string { return "" }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opts := sandbox.Options{WorkDir: t.TempDir(), RuntimeHome: t.TempDir(), Read: (Repo{}).Readable(), Env: []string{"PATH=" + bin + ":/usr/bin:/bin"}}
	if err := os.Chmod(opts.RuntimeHome, 0700); err != nil {
		t.Fatal(err)
	}
	box, err := sandbox.Open(ctx, opts)
	if err != nil {
		if ctx.Err() != nil {
			t.Fatal("interrupted proof is not a supported refusal:", err)
		}
		var proof *sandbox.ProofError
		var refusal *sandbox.RefusalError
		if !errors.As(err, &proof) && !errors.As(err, &refusal) {
			t.Fatal(err)
		}
		if harness.Support(harness.OpenAICompatible, harness.Session, harness.Sandbox).Usable() && proof == nil {
			t.Fatal("fixture setup refusal is not a command proof:", err)
		}
		if proof != nil && proof.Code == sandbox.CapabilityProbeTimeout {
			t.Fatal("timed-out proof is not a supported refusal:", err)
		}
		t.Logf("proved boundary refused before launch: %v", err)
		return
	}
	defer func() {
		if err := box.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := box.Run(ctx, sandbox.CommandRequest{Command: "crew-fixture-go"})
	if err != nil || result.ExitCode != 0 || result.TimedOut || result.Truncated || result.Stdout != "granted-toolchain" {
		t.Fatalf("%+v %v", result, err)
	}
}
