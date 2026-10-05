package roles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

// Exercise production options and turn input independently of OS proof admission.
func TestCommandBackedAPIStartsWithFileToolsAbsent(t *testing.T) {
	real := commandSupport
	t.Cleanup(func() { commandSupport = real })
	commandSupport = func(harness.Engine, harness.Operation, harness.Feature) harness.Capability {
		return harness.Capability{Availability: harness.Native}
	}
	tools := []session.WorkbenchTool{
		{Name: "read_file", Capability: harness.Capability{Availability: harness.Unsupported, Reason: "file tools are off"}},
		{Name: "search_files", Capability: harness.Capability{Availability: harness.Unsupported, Reason: "file tools are off"}},
		{Name: "run_command", Capability: harness.Capability{Availability: harness.Native}},
	}
	s := toolReportingSession{fakeSession: &fakeSession{confirmed: true}, tools: tools}
	opened := false
	n := Native{open: func(_ context.Context, o session.Options, _ json.RawMessage) (conversation, session.Opened, error) {
		if o.Workbench == nil || o.Workbench.Commands == nil {
			t.Fatal("command-backed options lost commands")
		}
		opened = true
		return s, session.Opened{}, nil
	}}
	out, err := n.Run(context.Background(), Spec{Engine: "openai-compatible", RuntimeHome: t.TempDir(), WorkDir: t.TempDir(), Prompt: "Read artifact.txt with run_command"})
	if err != nil || !opened || out.Text != "Done." || len(out.UnavailableTools) != 2 || !slices.Contains(s.calls, "turn: Read artifact.txt with run_command") {
		t.Fatalf("turn did not start with file tools absent: %+v %v calls=%v", out, err, s.calls)
	}
}

func TestAPIRoleWithoutCommandsRefusesWithoutFallback(t *testing.T) {
	real := commandSupport
	t.Cleanup(func() { commandSupport = real })
	commandSupport = func(harness.Engine, harness.Operation, harness.Feature) harness.Capability {
		return harness.Capability{Availability: harness.Unsupported, Reason: "no command sandbox"}
	}
	called := false
	n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		called = true
		return &fakeSession{ref: &session.Ref{ID: "unexpected"}}, session.Opened{}, nil
	}}
	home := filepath.Join(t.TempDir(), "not-created")
	out, err := n.Run(context.Background(), Spec{Engine: "openai-compatible", RuntimeHome: home})
	if err == nil || !Permanent(err) || called || out.FailureStage != "launch" || out.Opening != nil {
		t.Fatalf("%+v %v called=%v", out, err, called)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("prelaunch refusal created runtime home: %v", err)
	}
}

type toolReportingSession struct {
	*fakeSession
	tools []session.WorkbenchTool
}

func (s toolReportingSession) Capabilities() session.Capabilities {
	return session.Capabilities{WorkbenchTools: s.tools}
}

func TestNativeReportsUnavailableToolsBeforeOpening(t *testing.T) {
	tools := []session.WorkbenchTool{
		{Name: "read_file", Capability: harness.Capability{Availability: harness.Unsupported, Reason: "file tools are off"}},
		{Name: "list_files", Capability: harness.Capability{Availability: harness.Native}},
		{Name: "edit_file", Capability: harness.Capability{Availability: harness.Unsupported, Reason: "file tools are off"}},
	}
	s := toolReportingSession{fakeSession: &fakeSession{ref: &session.Ref{ID: "fixture"}, confirmed: true}, tools: tools}
	n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		return s, session.Opened{}, nil
	}}
	spec := codexRound
	var report []session.WorkbenchTool
	spec.ToolReport = func(got []session.WorkbenchTool) { report = got }
	spec.Opening = func(session.Opened, session.Ref) error {
		if len(report) != 2 {
			t.Fatalf("opening preceded capability report: %v", report)
		}
		return nil
	}
	result, err := n.Run(context.Background(), spec)
	want := []session.WorkbenchTool{tools[0], tools[2]}
	if err != nil || !reflect.DeepEqual(result.UnavailableTools, want) || !reflect.DeepEqual(report, want) {
		t.Fatalf("%+v %v report=%v", result, err, report)
	}
	report[0].Name = "changed"
	tools[2].Name = "changed"
	if result.UnavailableTools[0].Name != "read_file" || result.UnavailableTools[1].Name != "edit_file" {
		t.Fatal("tool report aliases session or callback")
	}
}
