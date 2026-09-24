package roles

import (
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
