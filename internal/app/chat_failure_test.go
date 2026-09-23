package app

import (
	"errors"
	"github.com/shhac/crew-assistant/internal/engine"
	"strings"
	"testing"
)

func TestChatFailureGivesSafeNextStep(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{engine.ErrNotConfigured, "Choose an assistant model"},
		{errors.New("daily model call allowance exhausted"), "daily model-call allowance"},
		{errors.New("Codex request failed; PRIVATE-DIAGNOSTIC"), "Check the selected CLI"},
		{errors.New("private backend diagnostic PRIVATE-DIAGNOSTIC"), "could not finish"},
	} {
		got := chatFailureReason(tc.err)
		if !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE-DIAGNOSTIC") || !strings.Contains(got, "no automatic replay") {
			t.Fatal(got)
		}
	}
}
