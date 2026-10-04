package roles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	harness "github.com/shhac/lib-agent-harness"
	"github.com/shhac/lib-agent-harness/session"
)

type accountingSession struct {
	*fakeSession
	result                            session.Result
	startErr, terminalErr, releaseErr error
}

func TestNativeRetainsCleanupEvidenceAlongsideReleaseError(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			failure := errors.New("synthetic credential writeback failure")
			reported := session.Result{Status: "completed", TurnID: "turn", Usage: session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 12, Output: 3}, Final: true}}
			s := accountingSession{fakeSession: &fakeSession{confirmed: confirmed}, result: reported, releaseErr: failure}
			n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
				return s, session.Opened{}, nil
			}}
			spec := codexRound
			var ended bool
			spec.Ended = func(gone bool) { ended = gone }
			got, err := n.Run(context.Background(), spec)
			if !errors.Is(err, failure) || got.FailureStage != "release" || got.CleanupConfirmed != confirmed || ended != confirmed || !reflect.DeepEqual(got.Provider, reported) || got.Opening == nil {
				t.Fatalf("cleanup evidence lost: %+v ended=%v err=%v", got, ended, err)
			}
		})
	}
}

func (s accountingSession) StartTurn(ctx context.Context, in session.Input) (turn, error) {
	s.calls = append(s.calls, "submit")
	if s.startErr != nil {
		return nil, s.startErr
	}
	return fakeTurn{s: s.fakeSession, name: "work", result: s.result, err: s.terminalErr}, nil
}

func (s accountingSession) Release(ctx context.Context) (session.Reclamation, error) {
	s.calls = append(s.calls, "release")
	return session.Reclamation{Confirmed: s.confirmed}, s.releaseErr
}

func TestNativeRetainsOpeningAndAccountingOnEveryFailureStage(t *testing.T) {
	failure := errors.New("synthetic failure")
	usage := session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 50, CacheRead: 20, CacheWrite: 5, Output: 3}, Final: true}
	for _, stage := range []string{"opening_record", "compaction", "start", "acceptance_record", "wait", "release", ""} {
		t.Run(stage, func(t *testing.T) {
			base := &fakeSession{confirmed: true, compacted: session.Result{Status: "completed", Usage: usage}}
			s := accountingSession{fakeSession: base, result: session.Result{Status: "completed", TurnID: "provider-turn", Usage: usage, Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 99}}}}
			spec := codexRound
			spec.Compact = true
			opening := session.Opened{Resumed: true}
			n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
				return s, opening, nil
			}}
			spec.Opening = func(o session.Opened, ref session.Ref) error {
				if len(base.calls) != 0 || o != opening {
					t.Fatalf("opening arrived after work: %v", base.calls)
				}
				if stage == "opening_record" {
					return failure
				}
				return nil
			}
			spec.Accepted = func() error {
				if stage == "acceptance_record" {
					return failure
				}
				return nil
			}
			switch stage {
			case "compaction":
				base.waitErr = failure
			case "start":
				s.startErr = failure
			case "wait":
				s.terminalErr = failure
				s.result.Status = "failed"
				s.result.NativeError = true
			case "release":
				s.releaseErr = failure
			}
			got, err := n.Run(context.Background(), spec)
			if (err != nil) != (stage != "") || got.Opening == nil || *got.Opening != opening || got.FailureStage != stage {
				t.Fatalf("result %+v, err %v", got, err)
			}
			if stage != "opening_record" && !reflect.DeepEqual(got.Compaction.Usage, usage) {
				t.Fatalf("lost compaction: %+v", got)
			}
			if stage == "wait" || stage == "release" || stage == "acceptance_record" || stage == "" {
				if !reflect.DeepEqual(got.Provider, s.result) {
					t.Fatalf("lost terminal evidence: %+v", got)
				}
			}
			if stage == "opening_record" && len(base.calls) != 1 {
				t.Fatalf("inference after failed opening persistence: %v", base.calls)
			}
		})
	}
}

func TestNativeActualOpeningOutcomes(t *testing.T) {
	for _, outcome := range []session.Opened{{Resumed: true}, {}, {Fresh: session.FreshIncompatible}, {Fresh: session.FreshUnavailable}} {
		s := &fakeSession{confirmed: true}
		n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
			return s, outcome, nil
		}}
		spec := codexRound
		var heard session.Opened
		spec.Opening = func(o session.Opened, _ session.Ref) error { heard = o; return nil }
		got, err := n.Run(context.Background(), spec)
		if err != nil || got.Opening == nil || *got.Opening != outcome || heard != outcome {
			t.Fatalf("opening: %+v %v", got, err)
		}
	}
}

type cancelledAccountingSession struct {
	*fakeSession
	cancel context.CancelFunc
	result session.Result
}
type cancelledAccountingTurn struct{ result session.Result }

func (t cancelledAccountingTurn) Events() <-chan session.Event {
	c := make(chan session.Event)
	close(c)
	return c
}
func (t cancelledAccountingTurn) Wait(ctx context.Context) (session.Result, error) {
	if ctx.Err() != nil {
		return session.Result{}, ctx.Err()
	}
	return t.result, context.Canceled
}
func (s cancelledAccountingSession) StartTurn(context.Context, session.Input) (turn, error) {
	s.cancel()
	return cancelledAccountingTurn{result: s.result}, nil
}

func TestNativeCancellationCollectsTerminalAccountingBeforeEndingObserver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reported := session.Result{Status: "interrupted", TurnID: "interrupted-turn", Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 42}}}
	s := cancelledAccountingSession{fakeSession: &fakeSession{confirmed: true}, cancel: cancel, result: reported}
	n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		return s, session.Opened{Resumed: true}, nil
	}}
	spec := codexRound
	spec.Observer = &recordingObserver{}
	got, err := n.Run(ctx, spec)
	if !errors.Is(err, context.Canceled) || got.Provider != reported || got.Provider.Usage.Known || !got.CleanupConfirmed || got.Opening == nil {
		t.Fatalf("cancellation accounting: %+v %v", got, err)
	}
}

func TestNativeLaunchRefusalHasNoFreshOpeningOrMeasuredUsage(t *testing.T) {
	failure := &session.CapabilityError{Engine: harness.Claude, Code: session.CapabilityLoginUnavailable, Phase: session.BeforeLaunch}
	n := Native{open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
		return nil, session.Opened{}, failure
	}}
	result, err := n.Run(context.Background(), Spec{Engine: "claude"})
	if !errors.Is(err, failure) || result.FailureStage != "launch" || result.Opening != nil || result.Provider.Usage.Known {
		t.Fatalf("launch refusal: %+v %v", result, err)
	}
}

// The first Wait sees cancellation before terminal evidence arrives. Release
// then consumes its deadline; collecting evidence must get a fresh budget.
type deadlineAccountingSession struct {
	*fakeSession
	cancel context.CancelFunc
	result session.Result
}

func (s deadlineAccountingSession) Release(ctx context.Context) (session.Reclamation, error) {
	<-ctx.Done()
	return session.Reclamation{}, ctx.Err()
}

func (s deadlineAccountingSession) StartTurn(context.Context, session.Input) (turn, error) {
	s.cancel()
	return cancelledAccountingTurn{result: s.result}, nil
}

func (s deadlineAccountingSession) Compact(context.Context) (turn, error) {
	s.cancel()
	return cancelledAccountingTurn{result: s.result}, nil
}

func TestCancellationCollectsWorkAndCompactionAfterReleaseDeadline(t *testing.T) {
	for _, compaction := range []bool{false, true} {
		t.Run(fmt.Sprint(compaction), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reported := session.Result{Status: "interrupted", TurnID: "settled", Usage: session.Usage{Usage: harness.Usage{Known: true, CacheKnown: true, Input: 18, CacheRead: 5, Output: 2}, Final: true}, Observed: session.Usage{Usage: harness.Usage{Known: true, Input: 11}}}
			s := deadlineAccountingSession{fakeSession: &fakeSession{}, cancel: cancel, result: reported}
			n := Native{cleanupTimeout: time.Millisecond, open: func(context.Context, session.Options, json.RawMessage) (conversation, session.Opened, error) {
				return s, session.Opened{Resumed: true}, nil
			}}
			spec := codexRound
			spec.Compact = compaction
			got, err := n.Run(ctx, spec)
			if !errors.Is(err, context.Canceled) || got.CleanupConfirmed {
				t.Fatalf("result %+v: %v", got, err)
			}
			evidence := got.Provider
			if compaction {
				evidence = got.Compaction
				if got.Provider.Usage.Known {
					t.Fatal("compaction counted as work")
				}
			}
			if evidence != reported {
				t.Fatalf("lost settled evidence: %+v", got)
			}
		})
	}
}
