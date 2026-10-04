package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/lifecycle"
	"github.com/shhac/crew-assistant/internal/upgrade"
)

func TestAutomaticAdmissionRevalidatesCandidateAndQuietMoment(t *testing.T) {
	for _, change := range []string{"skip", "off", "superseded", "chat", "role", "cap-chat", "cap-skip", "quiet"} {
		t.Run(change, func(t *testing.T) {
			a := testApp(t)
			ctx := context.Background()
			a.version = "v1.0.0"
			cfg := a.Config()
			cfg.Upgrade.Mode = "ask"
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			if err := a.Core.RecordUpdateCheck(ctx, a.version, upgrade.Result{Available: "v2.0.0"}); err != nil {
				t.Fatal(err)
			}
			initial, err := a.Core.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Upgrade.Mode = "automatic"
			if err := a.UpdateConfig(cfg); err != nil {
				t.Fatal(err)
			}
			stop, end := lifecycle.Watch(ctx, make(chan os.Signal), func(string) {})
			defer end()
			a.setStop(stop)
			a.upgradeEngine = &upgrade.Engine{Path: upgrade.RecordPath(filepath.Join(t.TempDir(), "state.db")), Drain: func() bool { return stop.Drain("upgrade") }}
			requests := 0
			a.requestUpgrade = func(v string, automatic bool) error {
				requests++
				return a.upgradeEngine.Request(upgrade.Record{From: a.version, To: v, Automatic: automatic})
			}
			// The scheduler has already observed this candidate and quiet
			// moment. Change its inputs before final admission, deterministically.
			if !automaticEligible(initial, a.version, "v2.0.0") {
				t.Fatal(initial.Update)
			}
			switch change {
			case "skip", "cap-skip":
				d := initial.Decisions[len(initial.Decisions)-1]
				if _, err := a.Core.ChooseDecision(ctx, d.ID, "Skip v2.0.0", core.FromOwner); err != nil {
					t.Fatal(err)
				}
			case "off":
				cfg.Upgrade.Mode = "off"
				if err := a.UpdateConfig(cfg); err != nil {
					t.Fatal(err)
				}
			case "superseded":
				if err := a.Core.RecordUpdateCheck(ctx, a.version, upgrade.Result{Available: "v3.0.0"}); err != nil {
					t.Fatal(err)
				}
			case "chat", "cap-chat":
				if _, err := a.Core.EnqueueChat(ctx, "busy", "owner reply"); err != nil {
					t.Fatal(err)
				}
				if _, err := a.Core.StartNextChat(ctx, cfg.AssistantHarness().Engine); err != nil {
					t.Fatal(err)
				}
			case "role":
				p := slackProject(t, a, true)
				if _, _, ok, err := a.Core.ClaimPMQuestion(ctx, p.ID, func(core.Role) string { return "" }); err != nil || !ok {
					t.Fatal(ok, err)
				}
			}
			admitted, err := a.tryAutomaticUpgrade(ctx, "v2.0.0", change == "cap-chat" || change == "cap-skip")
			want := change == "quiet" || change == "cap-chat"
			if err != nil || admitted != want || stop.Stopping() != want || requests != map[bool]int{true: 1, false: 0}[want] {
				t.Fatal(admitted, stop.Stopping(), requests, err)
			}
			if !want {
				if err := a.RequestUpgrade("v2.0.0", true); err != nil {
					t.Fatal(err)
				}
				if requests != 0 || stop.Stopping() {
					t.Fatal("public automatic request bypassed eligibility/quiet admission")
				}
			}
		})
	}
}
