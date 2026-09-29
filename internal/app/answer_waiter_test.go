package app

import (
	"errors"
	"testing"
	"time"
)

// A waiter already answered by a stop or a cancel must not hang the chat
// queue when its turn finishes afterwards.
func TestAnsweringAnAlreadyAnsweredWaiterDoesNotBlock(t *testing.T) {
	a := &App{}
	waiter := make(chan chatOutcome, 1)
	waiter <- chatOutcome{err: errors.New("closed")}
	a.chatWaiters.Store("turn", waiter)
	done := make(chan struct{})
	go func() {
		a.answerWaiter("turn", chatOutcome{err: errors.New("finished")})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("answering a full waiter blocked")
	}
	if got := <-waiter; got.err.Error() != "closed" {
		t.Fatalf("the first answer was replaced: %v", got.err)
	}
}
