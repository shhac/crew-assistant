package roles

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	harness "github.com/shhac/lib-agent-harness"
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
	if codex.Sandbox.Loopback || codex.Sandbox.Web || codex.Browser || reviewer.Sandbox.Loopback || reviewer.Browser {
		t.Fatalf("a role reached further than it was given: %+v %+v", codex, reviewer)
	}
}

// QA that runs the app is given this machine's own addresses and, when its
// seat says so, the engine's browser: nothing wider, and the rest of its
// sandbox as any QA's.
func TestQARunningTheAppGetsLoopbackAndTheBrowserOnly(t *testing.T) {
	qa := options(Spec{Engine: "claude", WorkDir: "/work", Write: true, Env: []string{"PORT=41234"}, Loopback: true, Browser: true})
	if qa.Sandbox == nil || !qa.Sandbox.Loopback || !qa.Sandbox.Write || qa.Sandbox.Web || !qa.Browser {
		t.Fatalf("QA running the app: %+v %+v", qa, qa.Sandbox)
	}
	if !slices.Contains(qa.Env, "PORT=41234") {
		t.Fatalf("QA was not told its port: %v", qa.Env)
	}
	noBrowser := options(Spec{Engine: "claude", WorkDir: "/work", Write: true, Loopback: true})
	if !noBrowser.Sandbox.Loopback || noBrowser.Browser {
		t.Fatalf("the browser was turned on unasked: %+v", noBrowser)
	}
}

// Role turns, and what they start, run at background priority wherever the
// harness can run them so, so the owner's own use of the machine comes
// first; where it can't, asking would refuse the session, so they don't ask.
func TestRolesRunAtBackgroundPriorityWhereTheHarnessCan(t *testing.T) {
	for _, engine := range []string{"claude", "codex", "grok"} {
		usable := harness.Support(harness.Engine(engine), harness.Session, harness.Background).Usable()
		if got := options(Spec{Engine: engine, WorkDir: "/work"}).Background; got != usable {
			t.Fatalf("%s: background %v, where the harness says %v", engine, got, usable)
		}
	}
	if runtime.GOOS != "windows" && !options(Spec{Engine: "claude", WorkDir: "/work"}).Background {
		t.Fatal("a Claude role should run at background priority here")
	}
	if options(Spec{Engine: "openai-compatible", WorkDir: "/work"}).Background {
		t.Fatal("an API engine has no process to lower")
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
