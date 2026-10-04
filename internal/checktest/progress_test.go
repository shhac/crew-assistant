package checktest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnicodeSkipProgressRoundTrip(t *testing.T) {
	dir := t.TempDir()
	input := stream(Event{Action: "output", Package: "fixture", Test: "TestUnicode", Output: "required capability denied: " + strings.Repeat("界🙂", 4000)}, Event{Action: "skip", Package: "fixture", Test: "TestUnicode"}, Event{Action: "pass", Package: "fixture"})
	r, err := ReadProgress(strings.NewReader(input), "darwin", func(r Report) error {
		return SaveProgress(filepath.Join(dir, "progress"), Progress{Invocation: "unicode", Stage: "Go tests", Report: r})
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := LoadProgress(dir, "progress", "unicode")
	if err != nil || p == nil || len(p.Report.Skips) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	if reason := p.Report.Skips[0].Reason; !utf8.ValidString(reason) || len(reason) > 1024 || reason != r.Skips[0].Reason {
		t.Fatalf("invalid reason: %q", reason)
	}
}

func TestSkipEvidenceBudgetCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress")
	var events []Event
	for i := range 900 {
		name := fmt.Sprintf("TestRequired/%04d/", i) + strings.Repeat("i", 2029)
		events = append(events, Event{Action: "output", Package: "p", Test: name, Output: strings.Repeat("r", 1024)}, Event{Action: "skip", Package: "p", Test: name})
	}
	r, err := ReadProgress(strings.NewReader(stream(events...)), "darwin", func(r Report) error {
		return SaveProgress(path, Progress{Invocation: "budget", Stage: "Go tests", Report: r})
	})
	if err == nil || !strings.Contains(err.Error(), "retention budget") || !r.EvidenceLimited || !r.Failed || r.Complete {
		t.Fatalf("missing capacity failure: %+v %v", r, err)
	}
	p, err := LoadProgress(dir, "progress", "budget")
	if err != nil || p == nil || !p.Report.EvidenceLimited || len(p.Report.Skips) != len(r.Skips) {
		t.Fatalf("checkpoint lost observed skips: %+v %v", p, err)
	}
	size := 0
	for i, skip := range r.Skips {
		size += SkipEvidenceSize(skip)
		if p.Report.Skips[i] != skip {
			t.Fatal("checkpoint changed identifying evidence")
		}
	}
	if size > MaxSkipEvidenceBytes {
		t.Fatal("exceeded aggregate retention budget", size)
	}
	// A workspace-written oversized checkpoint must also fail closed.
	p.Report.Skips = append(p.Report.Skips, p.Report.Skips...)
	if err := SaveProgress(path, *p); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProgress(dir, "progress", "budget"); err == nil || !strings.Contains(err.Error(), "retention budget") {
		t.Fatal("accepted oversized checkpoint", err)
	}
}

func TestLongAndMultipleSkipReasonsPreserveIdentities(t *testing.T) {
	var events []Event
	for i := range 100 {
		test := fmt.Sprintf("TestRequired/%03d", i)
		events = append(events, Event{Action: "output", Package: "fixture", Test: test, Output: "required capability loopback denied: " + strings.Repeat("reason", 4000)}, Event{Action: "skip", Package: "fixture", Test: test})
	}
	events = append(events, Event{Action: "pass", Package: "fixture"})
	r, err := Read(strings.NewReader(stream(events...)), "darwin")
	if err != nil || !r.Failed || len(r.Skips) != 100 {
		t.Fatalf("%+v %v", r, err)
	}
	var b bytes.Buffer
	r.Write(&b)
	for _, skip := range r.Skips {
		if len(skip.Reason) > 1024 || !strings.Contains(skip.Reason, "loopback denied") || !strings.Contains(b.String(), "REQUIRED coverage skipped: fixture "+skip.Test) {
			t.Fatalf("lost skip identity or reason: %+v", skip)
		}
	}
}

func TestDiagnosticIngestionBoundsAndPreservesEvidence(t *testing.T) {
	d := &diagnostic{tail: Tail{Limit: 4 << 10}}
	d.Write("capability reason at start\n")
	for range 10000 {
		d.Write(strings.Repeat("noise", 1000))
	}
	d.Write("failure evidence at end")
	if len(d.head) > 1024 || len(d.tail.String()) > (4<<10)+64 || !strings.Contains(d.String(), "capability reason") || !strings.Contains(d.String(), "failure evidence") {
		t.Fatal("unbounded or missing diagnostic", len(d.String()))
	}
	var events []Event
	for i := range 100 {
		test := fmt.Sprintf("TestBad%03d", i)
		events = append(events, Event{Action: "output", Package: "p", Test: test, Output: strings.Repeat("noise", 2000) + "assertion failed"}, Event{Action: "fail", Package: "p", Test: test})
	}
	events = append(events, Event{Action: "fail", Package: "p"})
	r, err := Read(strings.NewReader(stream(events...)), "darwin")
	if err != nil || !r.Failed {
		t.Fatal(err)
	}
	size := 0
	for _, text := range r.Failures {
		size += len(text)
	}
	if size > 32<<10 || !strings.Contains(strings.Join(r.Failures, "\n"), "TestBad099") || !strings.Contains(strings.Join(r.Failures, "\n"), "assertion failed") {
		t.Fatal("unbounded or missing failures", size)
	}
}

func TestProgressIsolationAndUnreadableEvidenceFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "progress")
	p := Progress{Invocation: "one", Stage: "Go tests", Report: Report{Skips: []Skip{{Package: "p", Test: "TestRequired", Reason: "capability denied"}}}}
	if err := SaveProgress(path, p); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProgress(dir, "progress", "one")
	if err != nil || loaded == nil || loaded.Done || len(loaded.Report.Skips) != 1 {
		t.Fatalf("%+v %v", loaded, err)
	}
	if _, err := LoadProgress(dir, "progress", "two"); err == nil {
		t.Fatal("accepted stale invocation")
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProgress(dir, "progress", "one"); err == nil {
		t.Fatal("accepted incomplete evidence")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), path); err == nil {
		if _, err := LoadProgress(dir, "progress", "one"); err == nil {
			t.Fatal("followed evidence symlink")
		}
	}
}

func TestProgressWriteFailureRejectsStream(t *testing.T) {
	input := stream(Event{Action: "output", Package: "p", Test: "TestRequired", Output: "required capability denied"}, Event{Action: "skip", Package: "p", Test: "TestRequired"}, Event{Action: "pass", Package: "p"})
	r, err := ReadProgress(strings.NewReader(input), "darwin", func(Report) error { return fmt.Errorf("synthetic write denial") })
	if err == nil || !strings.Contains(err.Error(), "synthetic write denial") || r.Complete || len(r.Skips) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
