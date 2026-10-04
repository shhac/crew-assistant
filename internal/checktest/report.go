// Package checktest enforces executed coverage in the project's Go check.
package checktest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type Event struct{ Action, Package, Test, Output, ImportPath string }
type Skip struct{ Package, Test, Reason, Exception string }
type Report struct {
	EvidenceLimited bool
	Skips           []Skip
	Failed          bool
	Complete        bool
	Failures        []string
	Executed        []string
}

func (s Skip) Write(w io.Writer) {
	classification := "REQUIRED coverage skipped"
	if s.Exception != "" {
		classification = "known skip: " + s.Exception
	}
	fmt.Fprintf(w, "%s: %s %s\n%s\n", classification, s.Package, s.Test, bounded(s.Reason, 1024))
}

// Read consumes go test -json, retaining reasons by full subtest name.
func Read(r io.Reader, platform string) (Report, error) {
	return ReadProgress(r, platform, nil)
}

// ReadProgress saves observed coverage without waiting for the producer to exit.
func ReadProgress(r io.Reader, platform string, changed func(Report) error) (Report, error) {
	report := Report{}
	output := map[string]*diagnostic{}
	failureBytes := 0
	skipBytes := 0
	rememberFailure := func(key, text string) {
		entry := key + "\n" + bounded(text, 4<<10)
		report.Failures = append(report.Failures, entry)
		failureBytes += len(entry)
		for failureBytes > 32<<10 && len(report.Failures) > 1 {
			failureBytes -= len(report.Failures[0])
			report.Failures = report.Failures[1:]
		}
	}
	clearPackage := func(pkg string) {
		for key := range output {
			if strings.HasPrefix(key, pkg+"/") {
				delete(output, key)
			}
		}
	}
	active := map[string]bool{}
	completed := 0
	sawOutput := false
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var e Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return report, fmt.Errorf("incomplete Go test stream: %w", err)
		}
		if e.Package == "" {
			e.Package = e.ImportPath
		}
		if len(e.Package)+len(e.Test) > 2048 {
			return report, fmt.Errorf("incomplete Go test stream: test identity exceeds 2048 bytes")
		}
		key := e.Package + "/" + e.Test
		switch e.Action {
		case "start":
			if len(active) >= 1024 && !active[e.Package] {
				return report, fmt.Errorf("incomplete Go test stream: active package limit exceeded")
			}
			active[e.Package] = true
		case "output", "build-output":
			sawOutput = true
			if output[key] == nil {
				if len(output) >= 1024 {
					return report, fmt.Errorf("incomplete Go test stream: retained output limit exceeded")
				}
				output[key] = &diagnostic{tail: Tail{Limit: 4 << 10}}
			}
			output[key].Write(e.Output)
		case "fail", "build-fail":
			if e.Test == "" {
				for other, text := range output {
					if other != key && strings.HasPrefix(other, e.Package+"/") {
						rememberFailure(other, text.String())
						delete(output, other)
					}
				}
			}
			report.Failed = true
			rememberFailure(key, output[key].String())
			delete(output, key)
			if e.Test == "" {
				delete(active, e.Package)
				completed++
			}
		case "pass":
			if e.Test != "" {
				delete(output, key)
			}
			if e.Test != "" && (strings.Contains(e.Package, "/upgrade") || strings.Contains(e.Test, "Upgrade") || strings.Contains(e.Test, "Watchdog") || strings.Contains(e.Test, "PreservedListener") || strings.Contains(e.Test, "Recovery") || strings.Contains(e.Test, "Alias") || strings.Contains(e.Test, "TestServeRunsRealBackupInstallerAndSessionHandover")) {
				report.Executed = append(report.Executed, key)
				if len(report.Executed) > 512 {
					report.Executed = report.Executed[1:]
				}
			}
			if e.Test == "" {
				delete(active, e.Package)
				completed++
			}
		case "skip":
			if e.Test == "" {
				delete(active, e.Package)
				completed++
				if strings.Contains(output[key].String(), "[no test files]") {
					delete(output, key)
					continue
				}
				e.Test = "(package)"
			}
			reason := skipReason(output[key].String())
			if reason == "" {
				reason = "missing skip reason"
			}
			exception := allowed(e.Package, e.Test, reason, platform)
			if output[key] != nil && output[key].truncated {
				exception = ""
			}
			reason = bounded(reason, 1024)
			if len(report.Skips) >= 2048 {
				return report, fmt.Errorf("incomplete Go test stream: skip evidence limit exceeded (2048 entries)")
			}
			skip := Skip{e.Package, e.Test, reason, exception}
			report.Skips = append(report.Skips, skip)
			skipBytes += SkipEvidenceSize(skip)
			if exception == "" {
				report.Failed = true
			}
			delete(output, key)
		}
		if e.Test == "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			clearPackage(e.Package)
		}
		if changed != nil && (e.Action == "skip" || e.Action == "fail" || e.Action == "build-fail" || (e.Action == "pass" && e.Test == "")) {
			if err := changed(report); err != nil {
				return report, fmt.Errorf("save check progress: %w", err)
			}
		}
		if skipBytes > MaxSkipEvidenceBytes-(32<<10) {
			report.Failed = true
			report.EvidenceLimited = true
			if changed != nil {
				if err := changed(report); err != nil {
					return report, fmt.Errorf("save check progress: %w", err)
				}
			}
			return report, fmt.Errorf("incomplete Go test stream: skip evidence retention budget reached; stopped collection with all %d observed skips retained", len(report.Skips))
		}
	}
	if err := scanner.Err(); err != nil {
		return report, fmt.Errorf("incomplete Go test stream: %w", err)
	}
	report.Complete = len(active) == 0 && completed > 0 && sawOutput
	if !report.Complete {
		return report, fmt.Errorf("incomplete Go test stream: missing package completion or no test output")
	}
	return report, nil
}

func skipReason(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "=== ") && !strings.HasPrefix(line, "--- SKIP:") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// Exceptions identify exact test/platform/reason matches. Process inspection
// exceptions must name LAH-29; assertion failures are never exceptions.
func allowed(pkg, test, reason, platform string) string {
	// testing prefixes diagnostics with the source location. Strip just that
	// prefix; extra messages and mixed fixture errors must never qualify.
	reason = skipLocation.ReplaceAllString(reason, "")
	if pkg == "github.com/shhac/crew-assistant/internal/cli" {
		for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
			if test == "TestGeneratedShellCompletionSyntax/"+shell && reason == "script generated; "+shell+" unavailable for syntax check" {
				return "optional shell: " + shell
			}
		}
	}
	if platform == "windows" {
		for _, entry := range windowsShellFixtures {
			if pkg == "github.com/shhac/crew-assistant/"+entry.pkg && test == entry.test && reason == "shell fixture" {
				return "unsupported platform: Windows shell fixture"
			}
		}
		if pkg == "github.com/shhac/crew-assistant/internal/upgrade" && test == "TestInstallerWithFakeHomebrewAndBinary" && reason == "shell fixture" {
			return "unsupported platform: Windows shell fixture"
		}
		if pkg == "github.com/shhac/crew-assistant/internal/core" && test == "TestProjectDirectorySymlinksDeduplicateAndScratchSymlinksFailClosed" && reason == "symlink creation requires Windows privileges" {
			return "unsupported platform: Windows symlink privileges"
		}
		// These tests skip only at their symlink-creation step. Windows may
		// require a privilege unavailable to an otherwise supported test host.
		for _, entry := range []struct{ pkg, test string }{
			{"internal/media/localdocs", "TestLinksAreNotFollowed"},
			{"internal/statepath", "TestEnsureDirectoryRefusesARootItCannotTrust"},
			{"internal/statepath", "TestEnsureDirectoryRefusesAComponentThatIsNotADirectory"},
			{"internal/filesystem", "TestSymlinkNavigationDoesNotReadFileContent"},
			{"internal/upgrade", "TestStateAliasesShareJournalAndRestoreDestination"},
			{"internal/cli", "TestAliasStartsEnforcePinRestoreAndStateLock/rolled-back/file"},
			{"internal/cli", "TestAliasStartsEnforcePinRestoreAndStateLock/rolled-back/directory"},
			{"internal/cli", "TestAliasStartsEnforcePinRestoreAndStateLock/rolling-back/file"},
			{"internal/cli", "TestAliasStartsEnforcePinRestoreAndStateLock/rolling-back/directory"},
			{"internal/cli", "TestCommandsDiscoverDaemonAndUpgradeThroughAliases/file/alias-start"},
			{"internal/cli", "TestCommandsDiscoverDaemonAndUpgradeThroughAliases/file/real-start"},
			{"internal/cli", "TestCommandsDiscoverDaemonAndUpgradeThroughAliases/directory/alias-start"},
			{"internal/cli", "TestCommandsDiscoverDaemonAndUpgradeThroughAliases/directory/real-start"},
		} {
			if pkg == "github.com/shhac/crew-assistant/"+entry.pkg && test == entry.test && windowsSymlinkPrivilege.MatchString(reason) {
				return "unsupported platform: Windows symlink privileges"
			}
		}
	}
	return ""
}

var skipLocation = regexp.MustCompile(`^[A-Za-z0-9_.-]+\.go:[0-9]+: `)
var windowsSymlinkPrivilege = regexp.MustCompile(`^unsupported capability: Windows symlink privilege \(ERROR_PRIVILEGE_NOT_HELD\): symlink [^\n]+: A required privilege is not held by the client\.$`)

// Exact callers of the shared shell fixture, including bridgeChatSpec callers.
var windowsShellFixtures = []struct{ pkg, test string }{
	{"internal/roles", "TestOwnerBrowserBridge"},
	{"internal/server", "TestBrowserBridgeStatus"},
	{"internal/work", "TestFakeCodexWithNoBridgeStillCompletesTurn"},
	{"internal/app", "TestChatReadsOnlyBridgeFromOwnerHome"},
	{"internal/app", "TestChatFallsBackAtOpenForMissingBridge"},
	{"internal/app", "TestChatBrowserlessReopenFailure"},
}

func (r Report) Write(w io.Writer) {
	executionTail := &Tail{Limit: 2 << 10}
	regressionTail := &Tail{Limit: 4 << 10}
	failureTail := &Tail{Limit: 5 << 10}
	for _, test := range r.Executed {
		fmt.Fprintln(executionTail, "EXECUTED:", test)
		for _, name := range []string{
			"TestServeRunsRealBackupInstallerAndSessionHandover",
			"TestServeReportsFailedStopPersistenceAfterRecoveryExec",
			"TestServeSignalDuringRecoveryConfirmation",
			"TestAliasStartsEnforcePinRestoreAndStateLock",
			"TestStateAliasesShareJournalAndRestoreDestination",
			"TestWatchdogCancellationBoundaries",
			"TestWatchdogCancellationDuringRestorePreservesPendingRestart",
			"TestWatchdogReportsPersistentUnknownIdentityPastDeadline",
			"TestWatchdogRetriesUnknownLiveProcessIdentity",
		} {
			if strings.HasSuffix(test, "/"+name) || (name == "TestServeRunsRealBackupInstallerAndSessionHandover" && strings.Contains(test, "/"+name+"/")) {
				fmt.Fprintln(regressionTail, "EXECUTED:", test)
			}
		}
	}
	failures := 0
	for _, s := range r.Skips {
		if s.Exception == "" {
			failures++
		}
	}
	for _, failure := range r.Failures {
		fmt.Fprintln(failureTail, "FAILED:", failure)
	}
	fmt.Fprint(w, executionTail.String())
	fmt.Fprint(w, regressionTail.String())
	fmt.Fprint(w, failureTail.String())
	for _, s := range r.Skips {
		s.Write(w)
	}
	fmt.Fprintf(w, "Go check skip count: %d; required-skip failures: %d; complete: %t\n", len(r.Skips), failures, r.Complete)
}
