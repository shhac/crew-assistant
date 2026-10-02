package connections

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shhac/crew-assistant/internal/config"
	"github.com/shhac/crew-assistant/internal/core"
)

func TestLinRecordsEveryWriteOutcomeAndNoReadOrRefusal(t *testing.T) {
	store, err := core.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := core.NewService(store, config.Default())
	ctx := context.Background()
	invoked := 0
	c := Client{Run: func(context.Context, string, []string) ([]byte, error) { return []byte(`{"alias":"home"}`), nil }}
	call := LinCall{ConnectionID: "linear", Profile: "home", Args: []string{"issue", "comment", "new", "ENG-1", "private body"}, Writes: true}
	for _, result := range []LinResult{{Output: "created"}, {Failed: true, Error: "remote failure private body"}, {Failed: true, Unknown: true, Error: "lin did not finish in 30s"}} {
		c.RunClipped = func(context.Context, string, []string) LinResult { invoked++; return result }
		out, err := c.LinRecorded(ctx, service, linBindings(true), call, "", "PM")
		if err != nil || out.Failed != result.Failed {
			t.Fatalf("%+v %v", out, err)
		}
	}
	snap, _ := service.Snapshot(ctx)
	var summaries []string
	for _, entry := range snap.Activity {
		if entry.Kind == "linear.write" {
			summaries = append(summaries, entry.Summary)
		}
	}
	all := strings.Join(summaries, "\n")
	if len(summaries) != 6 || strings.Contains(all, "private body") || !strings.Contains(all, ": done") || !strings.Contains(all, ": failed") || !strings.Contains(all, ": outcome unknown") {
		t.Fatalf("Activity: %v", summaries)
	}
	if _, err := c.LinRecorded(ctx, service, linBindings(false), call, "", "PM"); err == nil || invoked != 3 {
		t.Fatal("refused write ran")
	}
	call.Args = []string{"issue", "get", "ENG-1"}
	if _, err := c.LinRecorded(ctx, service, linBindings(false), call, "", "PM"); err != nil {
		t.Fatal(err)
	}
	snap, _ = service.Snapshot(ctx)
	if len(snap.Activity) != 6 {
		t.Fatal("read or refusal created write Activity")
	}
}
