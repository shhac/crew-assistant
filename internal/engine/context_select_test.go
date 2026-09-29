package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestContextOptionsDefaultsAreValid(t *testing.T) {
	opts := ContextOptions{}.withDefaults()
	want := ContextOptions{MaxBytes: 128 * 1024, TriggerBytes: 96 * 1024, RetainTurns: 2, MaxSummaryBytes: 8 * 1024}
	if opts != want || !opts.valid() {
		t.Fatal(opts)
	}
	small := ContextOptions{MaxBytes: 4096}.withDefaults()
	if small.MaxSummaryBytes != 1024 || small.TriggerBytes != 3072 {
		t.Fatal(small)
	}
}

func TestContextOptionsValidRejectsUnsafeLimits(t *testing.T) {
	base := ContextOptions{MaxBytes: 4096, TriggerBytes: 3000, RetainTurns: 1, MaxSummaryBytes: 1024}
	if !base.valid() {
		t.Fatal("base limits rejected")
	}
	for name, mutate := range map[string]func(*ContextOptions){
		"tiny budget":          func(o *ContextOptions) { o.MaxBytes = 1023 },
		"trigger above budget": func(o *ContextOptions) { o.TriggerBytes = 4097 },
		"trigger below one":    func(o *ContextOptions) { o.TriggerBytes = 0 },
		"no retained turn":     func(o *ContextOptions) { o.RetainTurns = 0 },
		"tiny summary":         func(o *ContextOptions) { o.MaxSummaryBytes = 127 },
		"summary over half":    func(o *ContextOptions) { o.MaxSummaryBytes = 2049 },
	} {
		opts := base
		mutate(&opts)
		if opts.valid() {
			t.Error(name)
		}
	}
}

func TestRetainFewerTurnsOnlyUnderHardPressure(t *testing.T) {
	opts := ContextOptions{MaxBytes: 1000, RetainTurns: 2}
	if opts.retainFewerTurns(1000) || opts.RetainTurns != 2 {
		t.Fatal("shrank without hard pressure")
	}
	if !opts.retainFewerTurns(1001) || opts.RetainTurns != 1 {
		t.Fatal("did not shrink under hard pressure")
	}
	if opts.retainFewerTurns(1001) || opts.RetainTurns != 1 {
		t.Fatal("gave up the newest turn")
	}
}

func TestEligibleBoundarySkipsUnresolvedGroups(t *testing.T) {
	groups := []contextGroup{{resolved: true}, {resolved: true}, {resolved: false}, {resolved: true}}
	for _, test := range []struct {
		retain int
		want   int
		ok     bool
	}{{1, 3, true}, {2, 1, true}, {3, 0, false}, {4, 4, false}} {
		got, ok := eligibleBoundary(groups, test.retain)
		if got != test.want || ok != test.ok {
			t.Error(test.retain, got, ok)
		}
	}
}

func TestSelectSourceTakesResolvedGroupsWithinBudget(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "owner"},
		contextCall("a"), {Role: "tool", ToolCallID: "a", Content: "small"},
		contextCall("big"), {Role: "tool", ToolCallID: "big", Content: strings.Repeat("x", 5000)},
		contextCall("open"),
		contextCall("b"), {Role: "tool", ToolCallID: "b", Content: "small"},
	}
	selection := selectSource(messages, contextGroups(messages), 4000, 1024)
	if selection.first != 1 {
		t.Fatal(selection.first)
	}
	if want := []Message{messages[1], messages[2], messages[6], messages[7]}; !reflect.DeepEqual(selection.source, want) {
		t.Fatal(selection.source)
	}
	if !reflect.DeepEqual(selection.selected, map[int]bool{1: true, 2: true, 6: true, 7: true}) {
		t.Fatal(selection.selected)
	}
	if empty := selectSource(messages, nil, 4000, 1024); empty.first != -1 || len(empty.source) != 0 {
		t.Fatal(empty)
	}
}

func TestSpliceSummaryReplacesSelectedAtFirstPosition(t *testing.T) {
	messages := []Message{{Role: "user", Content: "0"}, {Role: "assistant", Content: "1"}, {Role: "user", Content: "2"}, {Role: "assistant", Content: "3"}}
	got := spliceSummary(messages, map[int]bool{1: true, 3: true}, 1, "summary")
	want := []Message{messages[0], {Role: "assistant", Content: "summary"}, messages[2]}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
