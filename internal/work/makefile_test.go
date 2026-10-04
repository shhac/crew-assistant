//go:build !windows

package work

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The npm launcher deliberately fails. A working Node can run its CLI
// directly, including when PATH contains spaces, without dropping any check.
func TestMakeFrontendCommandsInvokeNpmThroughNode(t *testing.T) {
	t.Parallel()
	makefile, err := filepath.Abs(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"npm":  "#!/bin/sh\nexit 79\n",
		"node": "#!/bin/sh\n[ \"$1\" = \"$CREW_TEST_NPM_CLI\" ] || exit 80\nshift\nprintf 'npm:%s\\n' \"$*\" >> \"$CREW_MAKE_LOG\"\n",
		"go":   "#!/bin/sh\nprintf 'go:%s\\n' \"$*\" >> \"$CREW_MAKE_LOG\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "commands")
	cmd := exec.Command("make", "-f", makefile, "VERSION=fixture", "check", "dashboard")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "CREW_TEST_NPM_CLI=" + filepath.Join(bin, "npm"), "CREW_MAKE_LOG=" + log}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make: %v\n%s", err, out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"go:vet ./...",
		"go:test ./... -count=1 -timeout 30m",
		"npm:--prefix internal/dashboard/ui run check",
		"npm:--prefix internal/dashboard/ui run check:bundle",
		"npm:--prefix internal/dashboard/ui test",
		"npm:--prefix internal/dashboard/ui run build",
	}
	if string(data) != strings.Join(want, "\n")+"\n" {
		t.Fatalf("commands: %s", data)
	}
}
