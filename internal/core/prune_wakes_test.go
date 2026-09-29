package core

import (
	"fmt"
	"slices"
	"testing"
)

func TestPruningWakesDropsOnlyTheOldestFinished(t *testing.T) {
	finishedStatuses := []string{WakeDelivered, WakeCancelled}
	tests := []struct {
		name     string
		finished int
		// liveEvery places a waiting or fired wake after every nth finished
		// one, so live wakes sit among the oldest and the newest alike.
		liveEvery int
	}{
		{name: "at the limit keeps everything", finished: keepFinished, liveEvery: 7},
		{name: "one over drops one", finished: keepFinished + 1, liveEvery: 5},
		{name: "far over drops the surplus", finished: keepFinished + 57, liveEvery: 3},
		{name: "live wakes only at the front", finished: keepFinished + 10, liveEvery: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var wakes []Wake
			var live []string
			liveStatus := []string{WakeWaiting, WakeFired}
			if tc.liveEvery == 0 {
				for i := range 4 {
					w := Wake{ID: fmt.Sprintf("live-%d", i), Status: liveStatus[i%2]}
					wakes = append(wakes, w)
					live = append(live, w.ID)
				}
			}
			for i := range tc.finished {
				wakes = append(wakes, Wake{ID: fmt.Sprintf("done-%03d", i), Status: finishedStatuses[i%2]})
				if tc.liveEvery > 0 && i%tc.liveEvery == 0 {
					w := Wake{ID: fmt.Sprintf("live-%03d", i), Status: liveStatus[(i/tc.liveEvery)%2]}
					wakes = append(wakes, w)
					live = append(live, w.ID)
				}
			}
			dropped := max(tc.finished-keepFinished, 0)
			var want []string
			seenFinished := 0
			for _, w := range wakes {
				if w.Status == WakeDelivered || w.Status == WakeCancelled {
					seenFinished++
					if seenFinished <= dropped {
						continue
					}
				}
				want = append(want, w.ID)
			}

			v := Snapshot{Wakes: wakes}
			pruneWakes(&v)

			var got []string
			finished := 0
			for _, w := range v.Wakes {
				got = append(got, w.ID)
				if w.Status == WakeDelivered || w.Status == WakeCancelled {
					finished++
				}
			}
			if finished != min(tc.finished, keepFinished) {
				t.Fatalf("%d finished wakes remain, want %d", finished, min(tc.finished, keepFinished))
			}
			for _, id := range live {
				if !slices.Contains(got, id) {
					t.Fatalf("live wake %s was pruned", id)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("pruned wakes out of order or the wrong ones dropped:\n got %v\nwant %v", got, want)
			}
		})
	}
}
