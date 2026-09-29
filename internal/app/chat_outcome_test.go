package app

import (
	"errors"
	"testing"

	"github.com/shhac/crew-assistant/internal/engine"
)

func TestChatOutcomeStatus(t *testing.T) {
	failure := errors.New("boom")
	for _, test := range []struct {
		name           string
		runErr         error
		stopped        bool
		status, reason string
	}{
		{"completed", nil, false, "completed", ""},
		{"completed despite a stop", nil, true, "completed", ""},
		{"failed", failure, false, "failed", chatFailureReason(failure)},
		{"interrupted", failure, true, "interrupted", "The assistant stopped before this message finished. Recorded actions were preserved; no automatic replay was attempted."},
	} {
		status, reason := chatOutcomeStatus(test.runErr, test.stopped)
		if status != test.status || reason != test.reason {
			t.Errorf("%s: %q %q", test.name, status, reason)
		}
	}
}

func TestAnswerWaiterReachesOnlyAWaitingCaller(t *testing.T) {
	a := &App{}
	a.answerWaiter("nobody", chatOutcome{err: errors.New("unheard")})
	waiting := make(chan chatOutcome, 1)
	a.chatWaiters.Store("turn", waiting)
	a.answerWaiter("turn", chatOutcome{result: engine.Result{Message: "done"}})
	if out := <-waiting; out.result.Message != "done" || out.err != nil {
		t.Fatal(out)
	}
}
