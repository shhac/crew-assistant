package work

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/diagnostics"
)

// shadowParity fails the test if, on any pass of the loop, a task's flow
// reads its record differently from the loop: the scenario the test scripts
// is one the flows already describe. A loop restarted from this one keeps
// the check.
func shadowParity(t *testing.T, a *Loop) {
	t.Helper()
	var mu sync.Mutex
	seen := map[core.FlowDisagreement]bool{}
	var found []core.FlowDisagreement
	a.shadowed = func(ds []core.FlowDisagreement) {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range ds {
			if !seen[d] {
				seen[d] = true
				found = append(found, d)
			}
		}
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range found {
			t.Errorf("the flow disagrees with the loop on task %s, revision %d: %s: flow %q, loop %q", d.TaskID, d.Revision, d.Kind, d.Flow, d.Engine)
		}
	})
}

// shadowEverywhere has every test loop check shadow parity, for a sweep of
// the whole suite: CREW_FLOW_SHADOW_ALL=1 go test ./internal/work.
var shadowEverywhere = os.Getenv("CREW_FLOW_SHADOW_ALL") != ""

// The shadow looks on every pass, and reports each disagreement it finds
// once, however many passes find it again.
func TestTheFlowShadowWatchesEveryPassAndReportsADisagreementOnce(t *testing.T) {
	t.Parallel()
	a, _, _ := loopApp(t, &scriptedRunner{reviews: []string{pass}}, "")
	looks := 0
	a.shadowed = func([]core.FlowDisagreement) { looks++ }
	var out bytes.Buffer
	a.Diagnostics = diagnostics.New(&out)
	settle(t, a)
	if looks == 0 || out.Len() != 0 {
		t.Fatalf("%d looks, reported %q", looks, out.String())
	}
	// A draft team has no researcher, so research has no place in its flow.
	snap, err := a.Core.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snap.Tasks[0].Status = core.TaskResearching
	a.shadowFlows(&snap)
	a.shadowFlows(&snap)
	if lines := strings.Count(out.String(), "\n"); lines != 1 || !strings.Contains(out.String(), `"stage":"flow_shadow"`) || !strings.Contains(out.String(), "no_stage") {
		t.Fatalf("reported %q", out.String())
	}
}
