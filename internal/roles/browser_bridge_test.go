package roles

import (
	"context"
	"errors"
	"testing"

	"github.com/shhac/crew-assistant/internal/testbridge"
	"github.com/shhac/lib-agent-harness/session"
)

func TestBrowserBridgeOptions(t *testing.T) {
	for _, engine := range []string{"codex", "claude", "openai-compatible"} {
		for _, browser := range []bool{false, true} {
			o := options(Spec{Engine: engine, Browser: browser, BridgeHome: "/owner"})
			want := ""
			if browser && engine == "codex" {
				want = "/owner"
			}
			if o.BrowserBridgeHome != want {
				t.Fatalf("%s browser=%v: %q", engine, browser, o.BrowserBridgeHome)
			}
		}
	}
}

func TestOwnerBrowserBridge(t *testing.T) {
	binary, owner, crew := testbridge.Fixture(t)
	spec := Spec{Engine: "codex", Binary: binary, Home: crew, BridgeHome: owner, WorkDir: t.TempDir(), RuntimeHome: t.TempDir(), Browser: true}
	if err := CheckBrowserBridge(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	spec.BridgeHome = ""
	err := CheckBrowserBridge(context.Background(), spec)
	var c *session.CapabilityError
	if !errors.As(err, &c) || c.Reason != session.BridgeNotDeclared {
		t.Fatalf("got %v", err)
	}
}

func TestBrowserUnusable(t *testing.T) {
	if _, ok := BrowserUnusable(&session.UnsupportedError{Operation: "browser_bridge_home", Code: session.RefusedConflict}, "/owner"); !ok {
		t.Fatal("bridge home refusal not recognized")
	}
	if _, ok := BrowserUnusable(&session.UnsupportedError{Operation: "sandbox", Code: session.RefusedConflict}, ""); ok {
		t.Fatal("non-browser refusal recognized")
	}

	for _, reason := range []string{session.BridgeHomeUnavailable, session.BridgeNotDeclared, session.BridgeDeclarationUnsupported, session.BridgeInstallationMissing, session.BridgeInWorkspace} {
		text, ok := BrowserUnusable(&session.CapabilityError{Code: session.CapabilityBrowserBridgeUnavailable, Reason: reason}, "/owner")
		if !ok || text == "" {
			t.Fatalf("%s: %q %v", reason, text, ok)
		}
	}
	for _, code := range []string{session.CapabilityBrowserToolsMissing, session.CapabilityBrowserSandboxUnproven, session.CapabilityBrowserSandboxNotEnforced} {
		if _, ok := BrowserUnusable(&session.CapabilityError{Code: code}, ""); !ok {
			t.Fatal(code)
		}
	}
	for _, err := range []error{errors.New("failure"), &session.CapabilityError{Code: "other"}} {
		if _, ok := BrowserUnusable(err, ""); ok {
			t.Fatal(err)
		}
	}
}
