package checktest

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequiredSkipFixture(t *testing.T) {
	if os.Getenv("CREW_SKIP_FIXTURE") == "1" {
		t.Skip("required capability fixture-execution unavailable: synthetic denial")
	}
}

func TestRealSkippedTestFailsCoverage(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "tool", "test2json", "-p", "fixture", binary, "-test.v", "-test.run=^TestRequiredSkipFixture$")
	cmd.Env = append(os.Environ(), "CREW_SKIP_FIXTURE=1")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("producer failed: %v %s", err, output)
	}
	report, err := Read(bytes.NewReader(output), "darwin")
	if err != nil || !report.Failed || len(report.Skips) != 1 {
		t.Fatalf("successful skipped subprocess accepted: %+v %v\n%s", report, err, output)
	}
}

func stream(events ...Event) string {
	var b bytes.Buffer
	for _, e := range events {
		json.NewEncoder(&b).Encode(e)
	}
	return b.String()
}

func TestRequiredSkipRejectsSuccessfulProducer(t *testing.T) {
	input := stream(Event{Action: "start", Package: "fixture"}, Event{Action: "output", Package: "fixture", Test: "TestRequired/child", Output: "required capability loopback unavailable: permission denied\n"}, Event{Action: "skip", Package: "fixture", Test: "TestRequired/child"}, Event{Action: "pass", Package: "fixture"})
	r, err := Read(strings.NewReader(input), "darwin")
	if err != nil || !r.Failed || !r.Complete || len(r.Skips) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	var b bytes.Buffer
	r.Write(&b)
	for _, want := range []string{"skip count: 1", "required-skip failures: 1", "TestRequired/child", "loopback", "permission denied"} {
		if !strings.Contains(b.String(), want) {
			t.Fatal(b.String())
		}
	}
}

func TestStreamFailuresAndZeroSkips(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		failed, bad bool
	}{
		{"zero", stream(Event{Action: "start", Package: "p"}, Event{Action: "output", Package: "p", Output: "ok"}, Event{Action: "pass", Package: "p"}), false, false},
		{"test failure", stream(Event{Action: "output", Package: "p", Test: "TestBad", Output: "assertion"}, Event{Action: "fail", Package: "p", Test: "TestBad"}, Event{Action: "fail", Package: "p"}), true, false},
		{"missing reason", stream(Event{Action: "output", Package: "p", Output: "ok"}, Event{Action: "skip", Package: "p", Test: "TestBad"}, Event{Action: "pass", Package: "p"}), true, false},
		{"output without completion", stream(Event{Action: "output", Package: "p", Output: "partial output"}), false, true},
		{"truncated", stream(Event{Action: "start", Package: "p"}), false, true},
		{"malformed", "{", false, true}, {"startup", "", false, true},
		{"package skip", stream(Event{Action: "output", Package: "p", Output: "skipped package coverage"}, Event{Action: "skip", Package: "p"}), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Read(strings.NewReader(tc.input), "darwin")
			if (err != nil) != tc.bad || r.Failed != tc.failed {
				t.Fatalf("%+v %v", r, err)
			}
			if tc.name == "zero" {
				var b bytes.Buffer
				r.Write(&b)
				if !strings.Contains(b.String(), "skip count: 0") {
					t.Fatal(b.String())
				}
			}
		})
	}
}

func TestExceptionPolicy(t *testing.T) {
	pkg := "github.com/shhac/crew-assistant/internal/upgrade"
	test := "TestInstallerWithFakeHomebrewAndBinary"
	for _, tc := range []struct {
		pkg, test, reason, platform string
		allowed                     bool
	}{
		{pkg, test, "shell fixture", "windows", true}, {pkg, test, "shell fixture", "darwin", false}, {pkg, test, "permission denied", "windows", false}, {pkg, test + "/other", "shell fixture", "windows", false},
		{"other", test, "shell fixture", "windows", false},
	} {
		if got := allowed(tc.pkg, tc.test, tc.reason, tc.platform) != ""; got != tc.allowed {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	// A known test's assertion failure remains a failure.
	r, err := Read(strings.NewReader(stream(Event{Action: "output", Package: pkg, Test: test, Output: "shell fixture"}, Event{Action: "fail", Package: pkg, Test: test}, Event{Action: "fail", Package: pkg})), "windows")
	if err != nil || !r.Failed || len(r.Skips) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestBuildAndUnfinishedTestDiagnostics(t *testing.T) {
	for _, events := range [][]Event{
		{{Action: "build-output", ImportPath: "p", Output: "compiler: undefined symbol"}, {Action: "build-fail", ImportPath: "p"}, {Action: "fail", Package: "p"}},
		{{Action: "start", Package: "p"}, {Action: "output", Package: "p", Test: "TestHung", Output: "panic: test timed out\nstack trace"}, {Action: "fail", Package: "p"}},
	} {
		r, _ := Read(strings.NewReader(stream(events...)), "darwin")
		var b bytes.Buffer
		r.Write(&b)
		if !r.Failed || (!strings.Contains(b.String(), "undefined symbol") && !strings.Contains(b.String(), "stack trace")) {
			t.Fatal(b.String())
		}
	}
}

func TestRequiredSkipSurvivesVerboseFailure(t *testing.T) {
	r := Report{Complete: true, Failed: true,
		Skips:    []Skip{{Package: "p", Test: "TestRequired", Reason: "required capability loopback unavailable: denied"}},
		Failures: []string{strings.Repeat("assertion output\n", 10000)},
	}
	var b bytes.Buffer
	r.Write(&b)
	if b.Len() > 18<<10 {
		t.Fatalf("unbounded report: %d", b.Len())
	}
	for _, want := range []string{"TestRequired", "loopback", "denied", "skip count: 1"} {
		if !strings.Contains(b.String(), want) {
			t.Fatal(b.String())
		}
	}
}

func TestWindowsBridgeFixtureExceptions(t *testing.T) {
	for _, entry := range windowsShellFixtures {
		pkg := "github.com/shhac/crew-assistant/" + entry.pkg
		input := stream(Event{Action: "output", Package: pkg, Test: entry.test, Output: "fixture.go:15: shell fixture\n"}, Event{Action: "skip", Package: pkg, Test: entry.test}, Event{Action: "pass", Package: pkg})
		r, err := Read(strings.NewReader(input), "windows")
		if err != nil || r.Failed || len(r.Skips) != 1 || r.Skips[0].Exception == "" {
			t.Fatalf("%+v %v", r, err)
		}
		for _, tc := range []struct{ test, reason, platform string }{
			{entry.test, "fixture.go:15: shell fixture", "darwin"},
			{entry.test + "/unexpected", "fixture.go:15: shell fixture", "windows"},
			{entry.test, "fixture execution denied", "windows"},
		} {
			if allowed(pkg, tc.test, tc.reason, tc.platform) != "" {
				t.Fatalf("unexpected exception: %+v", tc)
			}
		}
	}
}

func TestWindowsAliasReceiverExceptions(t *testing.T) {
	pkg := "github.com/shhac/crew-assistant/internal/cli"
	for _, step := range []string{"rolled-back", "rolling-back"} {
		for _, alias := range []string{"file", "directory"} {
			test := "TestAliasStartsEnforcePinRestoreAndStateLock/" + step + "/" + alias
			reason := "unsupported capability: Windows symlink privilege (ERROR_PRIVILEGE_NOT_HELD): symlink target link: A required privilege is not held by the client."
			if allowed(pkg, test, reason, "windows") == "" || allowed(pkg, test, reason, "darwin") != "" || allowed(pkg, test, "permission denied", "windows") != "" {
				t.Fatal(test)
			}
		}
	}
}

func TestKnownSkipReasonsRejectMixedFixtureErrors(t *testing.T) {
	for _, tc := range []struct{ pkg, test, reason, platform string }{
		{"internal/upgrade", "TestInstallerWithFakeHomebrewAndBinary", "shell fixture", "windows"},
		{"internal/roles", "TestOwnerBrowserBridge", "shell fixture", "windows"},
		{"internal/core", "TestProjectDirectorySymlinksDeduplicateAndScratchSymlinksFailClosed", "symlink creation requires Windows privileges", "windows"},
		{"internal/statepath", "TestEnsureDirectoryRefusesARootItCannotTrust", "unsupported capability: Windows symlink privilege (ERROR_PRIVILEGE_NOT_HELD): symlink target link: A required privilege is not held by the client.", "windows"},
		{"internal/cli", "TestGeneratedShellCompletionSyntax/fish", "script generated; fish unavailable for syntax check", "darwin"},
	} {
		t.Run(tc.test, func(t *testing.T) {
			pkg := "github.com/shhac/crew-assistant/" + tc.pkg
			for _, reason := range []string{tc.reason, "fixture_test.go:28: " + tc.reason} {
				if allowed(pkg, tc.test, reason, tc.platform) == "" {
					t.Fatalf("legitimate skip rejected: %s", reason)
				}
			}
			for _, reason := range []string{
				"file exists: " + tc.reason, tc.reason + ": file exists", "disk full\nfixture_test.go:28: " + tc.reason,
				"fixture_test.go:28: disk full: " + tc.reason, tc.reason + "\nmissing path",
				"disk full fixture_test.go:28: " + tc.reason,
				"symlinks unavailable: symlink target link: file exists",
				"symlinks unavailable: symlink target link: no such file or directory",
				"symlinks unavailable: symlink target link: no space left on device",
				"symlink target link: A required privilege is not held by the client.",
			} {
				r, err := Read(strings.NewReader(stream(Event{Action: "output", Package: pkg, Test: tc.test, Output: reason}, Event{Action: "skip", Package: pkg, Test: tc.test}, Event{Action: "pass", Package: pkg})), tc.platform)
				if err != nil || !r.Failed || len(r.Skips) != 1 || r.Skips[0].Exception != "" || r.Skips[0].Reason != reason {
					t.Fatalf("fixture error accepted or lost: %+v %v", r, err)
				}
			}
		})
	}
}

func TestProgressRecoveryRejectsMixedKnownSkipReason(t *testing.T) {
	dir := t.TempDir()
	for _, reason := range []string{"completion_test.go:135: script generated; fish unavailable for syntax check", "completion_test.go:135: fixture failed: script generated; fish unavailable for syntax check"} {
		p := Progress{Invocation: "one", Report: Report{Skips: []Skip{{Package: "github.com/shhac/crew-assistant/internal/cli", Test: "TestGeneratedShellCompletionSyntax/fish", Reason: reason, Exception: "optional shell: fish"}}}}
		if err := SaveProgress(filepath.Join(dir, "progress"), p); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadProgress(dir, "progress", "one")
		if err != nil || loaded == nil {
			t.Fatal(err)
		}
		wantFailure := strings.Contains(reason, "fixture failed")
		if loaded.Report.Failed != wantFailure || (loaded.Report.Skips[0].Exception == "") != wantFailure {
			t.Fatalf("%+v", loaded)
		}
	}
}

// Only the upgrade primitives test's exact process-inspection refusal is a
// known skip, and it names its upstream task.
func TestProcessInspectionExceptionIsExact(t *testing.T) {
	pkg, test := "github.com/shhac/crew-assistant/internal/cli", "TestUpgradeProcessPrimitives"
	reason := "required capability process-inspection unavailable: ps cannot inspect processes here"
	if got := allowed(pkg, test, reason, "darwin"); got != "process inspection: LAH-29" {
		t.Fatalf("exception %q", got)
	}
	for _, c := range [][3]string{{pkg, test, reason + " (other)"}, {pkg, "TestOther", reason}, {"github.com/shhac/crew-assistant/internal/upgrade", test, reason}} {
		if got := allowed(c[0], c[1], c[2], "darwin"); got != "" {
			t.Errorf("%v: unexpected exception %q", c, got)
		}
	}
}
