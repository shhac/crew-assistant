package work

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/lib-agent-harness/sandbox"
)

// A claim held only because its finished turn's accounting failed is let go
// at startup even when the command sweep fails: its session had ended, and
// keeping it would hold its member in every project.
func TestAnAccountingHoldGoesAtStartupEvenWhenTheSweepFails(t *testing.T) {
	a, p, _ := loopApp(t, &scriptedRunner{}, "")
	ctx := context.Background()
	claims, err := a.Core.Schedule(ctx, func(r core.Role) string { return "" })
	if err != nil || len(claims) == 0 {
		t.Fatalf("no claim: %v %v", claims, err)
	}
	claim := claims[0]
	if err := a.Core.HoldClaim(ctx, claim.Task.ID, p.ID, claim.Claim.Token, accountingHold); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.Core.StateDirectory(), "commands", "commands", "interrupted"), 0700); err != nil {
		t.Fatal(err)
	}
	a.commands = func(context.Context, sandbox.Options) (commandSandbox, error) {
		return &fakeCommands{closeErr: errors.New("cleanup unavailable")}, nil
	}
	_ = a.resume(ctx)
	if held := taskByID(t, a, claim.Task.ID).Claims; len(held) != 0 {
		t.Fatalf("the accounting hold survived a failed sweep: %+v", held)
	}
}
