package roles

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/shhac/lib-agent-harness/session"
)

func TestEveryRoleRunsSandboxedWithOnlyWhatItWasGiven(t *testing.T) {
	codex := options(Spec{Engine: "codex", Home: "/login", RuntimeHome: "/runtime", WorkDir: "/work", Write: true, Read: []string{"/go/pkg/mod"}, Env: []string{"GOCACHE=/work/.crew/go"}, Instructions: "Be brief."})
	if codex.Sandbox == nil || !codex.Sandbox.Write || !slices.Equal(codex.Sandbox.Read, []string{"/go/pkg/mod"}) {
		t.Fatalf("sandbox %+v", codex.Sandbox)
	}
	if codex.RuntimeHome != "/runtime" || codex.WorkDir != "/work" || !slices.Equal(codex.Env, []string{"GOCACHE=/work/.crew/go"}) {
		t.Fatalf("codex options %+v", codex)
	}
	if codex.Instructions.Mode != session.Append || codex.Instructions.Text != "Be brief." {
		t.Fatalf("instructions %+v", codex.Instructions)
	}
	reviewer := options(Spec{Engine: "claude", RuntimeHome: "/runtime", WorkDir: "/work"})
	if reviewer.Sandbox == nil || reviewer.Sandbox.Write || reviewer.RuntimeHome != "" || reviewer.Instructions.Text != "" {
		t.Fatalf("a read-only Claude role got more than it was given: %+v", reviewer)
	}
}

// Grok's agent mode edits and runs commands without asking, so a Grok role
// answers what it is asked as its work needs, and Codex and Claude roles keep
// the policy their stored conversations were started with.
func TestAGrokRoleAnswersPermissionAsItsWorkNeeds(t *testing.T) {
	writer := options(Spec{Engine: "grok", WorkDir: "/work", Write: true})
	if writer.Policy.GrokPermission != session.GrokAllowWhenAsked || writer.Policy.GrokTelemetry != session.GrokTelemetryReduced {
		t.Fatalf("writer policy %+v", writer.Policy)
	}
	reviewer := options(Spec{Engine: "grok", WorkDir: "/work"})
	if reviewer.Policy.GrokPermission != session.GrokDenyWhenAsked || reviewer.Policy.GrokTelemetry != session.GrokTelemetryReduced {
		t.Fatalf("reviewer policy %+v", reviewer.Policy)
	}
	for _, engine := range []string{"codex", "claude"} {
		if o := options(Spec{Engine: engine, WorkDir: "/work", Write: true}); !reflect.DeepEqual(o.Policy, session.Policy{}) {
			t.Errorf("%s policy %+v", engine, o.Policy)
		}
	}
}

// A Grok session can't be sandboxed, so a Grok role is refused before
// anything starts for want of the sandbox, not for its permission policy.
func TestAGrokRoleIsRefusedForWantOfASandbox(t *testing.T) {
	err := VerifySandbox(context.Background(), Spec{Engine: "grok", Binary: filepath.Join(t.TempDir(), "grok"), WorkDir: t.TempDir(), Write: true})
	var refused *session.UnsupportedError
	if !errors.As(err, &refused) || refused.Operation != "sandbox" {
		t.Fatalf("got %v", err)
	}
}
