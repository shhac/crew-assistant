package checktest

import (
	"strings"
	"testing"
)

func TestTailKeepsTerminalSummary(t *testing.T) {
	b := &Tail{Limit: 100}
	b.Write([]byte(strings.Repeat("x", 200)))
	b.Write([]byte("Go check skip count: 0\n"))
	if !strings.HasSuffix(b.String(), "Go check skip count: 0\n") || len(b.String()) > 140 {
		t.Fatal(b.String())
	}
}
