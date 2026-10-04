package testutil

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestSymlinkResultFixture(t *testing.T) {
	mode := os.Getenv("CREW_SYMLINK_FIXTURE")
	if mode == "" {
		return
	}
	var cause error = syscall.Errno(1314)
	switch mode {
	case "exists":
		cause = os.ErrExist
	case "missing":
		cause = os.ErrNotExist
	case "full":
		cause = syscall.ENOSPC
	case "phrase":
		cause = errors.New("symlinks unavailable: file exists")
	case "denied":
		cause = os.ErrPermission
	}
	var err error = &os.LinkError{Op: "symlink", Old: "target", New: "link", Err: cause}
	if mode == "mixed" {
		err = errors.Join(err, os.ErrExist)
	}
	requireSymlinkResult(t, err, os.Getenv("CREW_SYMLINK_PLATFORM"))
}

func TestSymlinkErrorsOnlySkipMissingWindowsPrivilege(t *testing.T) {
	RequireSymlinkResult(t, nil)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"windows", "darwin"} {
		for _, mode := range []string{"privilege", "exists", "missing", "full", "phrase", "mixed", "denied"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				cmd := exec.Command(binary, "-test.v", "-test.run=^TestSymlinkResultFixture$")
				cmd.Env = append(os.Environ(), "CREW_SYMLINK_FIXTURE="+mode, "CREW_SYMLINK_PLATFORM="+platform)
				output, err := cmd.CombinedOutput()
				wantSkip := platform == "windows" && mode == "privilege"
				originalReason := map[string]string{"privilege": syscall.Errno(1314).Error(), "exists": os.ErrExist.Error(), "missing": os.ErrNotExist.Error(), "full": syscall.ENOSPC.Error(), "phrase": "symlinks unavailable: file exists", "mixed": os.ErrExist.Error(), "denied": os.ErrPermission.Error()}[mode]
				if !strings.Contains(string(output), originalReason) {
					t.Fatalf("lost original error %q: %s", originalReason, output)
				}
				if (err == nil) != wantSkip || strings.Contains(string(output), "--- SKIP:") != wantSkip || !strings.Contains(string(output), "symlink target link:") {
					t.Fatalf("lost error or wrong classification: %s %v", output, err)
				}
			})
		}
	}
}

func TestRequiredCapabilityFixture(t *testing.T) {
	if os.Getenv("CREW_CAPABILITY_FIXTURE") == "1" {
		SkipRequired(t, "loopback", "synthetic permission denial")
	}
}

func TestRequiredCapabilityPolicy(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, hosted := range []string{"0", "1"} {
		cmd := exec.Command(binary, "-test.v", "-test.run=^TestRequiredCapabilityFixture$")
		cmd.Env = append(os.Environ(), "CREW_CAPABILITY_FIXTURE=1", "CREW_HOSTED_CHECK="+hosted)
		output, err := cmd.CombinedOutput()
		if (err != nil) != (hosted == "1") || !strings.Contains(string(output), "loopback") || !strings.Contains(string(output), "synthetic permission denial") {
			t.Fatalf("hosted=%s: %s %v", hosted, output, err)
		}
	}
}
