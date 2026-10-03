package work

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/sandbox"
	"github.com/shhac/lib-agent-harness/session"
)

func TestCommandErrorMatchesSessionVocabulary(t *testing.T) {
	capability := harness.Capability{Availability: harness.Unsupported, Reason: "synthetic refusal"}
	plain := errors.New("plain error")
	cases := []struct {
		name        string
		input, want error
	}{
		{"nil", nil, nil},
		{"refusal", &sandbox.RefusalError{Operation: "run", Code: sandbox.RefusedNotOffered, Capability: capability}, &session.UnsupportedError{Engine: harness.OpenAICompatible, Operation: "run", Code: sandbox.RefusedNotOffered, Capability: capability}},
		{"closed", sandbox.ErrClosed, session.ErrClosed},
		{"wrapped closed", fmt.Errorf("wrapped: %w", sandbox.ErrClosed), session.ErrClosed},
		{"plain", plain, plain},
	}
	for _, code := range []string{sandbox.CapabilitySandboxUnavailable, sandbox.CapabilitySandboxToolOutdated} {
		var tools []string
		if code == sandbox.CapabilitySandboxToolOutdated {
			tools = []string{"bwrap"}
		}
		cases = append(cases, struct {
			name        string
			input, want error
		}{code, &sandbox.ProofError{Code: code, Tools: tools}, &session.CapabilityError{Engine: harness.OpenAICompatible, Code: code, Phase: session.BeforeLaunch, Tools: tools}})
	}
	for _, code := range []string{sandbox.CommandStartFailed, sandbox.CommandOutcomeUnknown, sandbox.CommandProcessLimit, sandbox.CommandCleanupUnknown, sandbox.CommandSandboxClosed} {
		cases = append(cases, struct {
			name        string
			input, want error
		}{code, &sandbox.CommandError{Code: code}, &session.TurnError{Engine: harness.OpenAICompatible, Code: code}})
	}
	for _, code := range []string{sandbox.StateLocked, sandbox.StateUnusable} {
		cases = append(cases, struct {
			name        string
			input, want error
		}{code, &sandbox.StateError{Code: code}, &session.StateError{Engine: harness.OpenAICompatible, Code: code}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, input := range []error{tc.input, wrapCommandTestError(tc.input)} {
				got := commandError(input)
				want := tc.want
				if tc.input == plain {
					want = input
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %T %v, want %T %v", got, got, want, want)
				}
				if got == nil {
					continue
				}
				if got.Error() != want.Error() || fmt.Sprintf("%T", got) != fmt.Sprintf("%T", want) {
					t.Fatal("error vocabulary changed")
				}
				for _, sentinel := range []error{session.ErrUnsupported, session.ErrTurnFailed, session.ErrClosed, session.ErrLeaseHeld} {
					if errors.Is(got, sentinel) != errors.Is(want, sentinel) {
						t.Fatalf("sentinel identity changed: %v", sentinel)
					}
				}
				for _, typ := range []reflect.Type{reflect.TypeOf((*session.UnsupportedError)(nil)), reflect.TypeOf((*session.CapabilityError)(nil)), reflect.TypeOf((*session.TurnError)(nil)), reflect.TypeOf((*session.StateError)(nil))} {
					g, w := reflect.New(typ).Interface(), reflect.New(typ).Interface()
					if errors.As(got, g) != errors.As(want, w) || !reflect.DeepEqual(g, w) {
						t.Fatalf("typed identity changed: %v", typ)
					}
				}
				if tc.input == plain && got != input {
					t.Fatal("plain error identity changed")
				}
			}
		})
	}
}

func wrapCommandTestError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("wrapped: %w", err)
}

func TestNoDeprecatedSessionCommandNames(t *testing.T) {
	deprecated := regexp.MustCompile(`session\.(OpenCommandSandbox|CommandSandboxOptions|CommandSandbox|CommandRequest|CommandResult|StartedCommand|CommandStartFailed|CommandOutcomeUnknown|CommandProcessLimit|CommandCleanupUnknown|CommandSandboxClosed)\b`)
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || filepath.Clean(path) == filepath.Clean("../../internal/work/commands_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if match := deprecated.Find(data); match != nil {
			t.Errorf("%s uses deprecated %s", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
