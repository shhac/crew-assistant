package work

import (
	"encoding/json"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
	"github.com/shhac/crew-assistant/internal/text"
)

func splitHandOn(reply string) (string, string) {
	clean, block := splitBlock(reply, "hand-on")
	if block != "" {
		return clean, block
	}
	if i := strings.LastIndex(reply, "```hand-on\n"); i >= 0 {
		return strings.TrimSpace(reply[:i]), "invalid hand-on block"
	}
	return clean, ""
}

func parseHandOn(block string) (string, []string) {
	if block == "" {
		return "", nil
	}
	var in struct {
		Why string `json:"why"`
	}
	if json.Unmarshal([]byte(block), &in) != nil || strings.TrimSpace(in.Why) == "" {
		return "", []string{"hand-on block ignored: expected an object with a nonempty why"}
	}
	return text.Clip(strings.TrimSpace(in.Why), 1000), nil
}

func handOnGuide(t core.Task, kind, group string) string {
	n := 0
	for _, r := range t.RolesOf(kind) {
		if group == "" || t.CheckerGroup(r.Name) == group {
			n++
		}
	}
	if n < 2 {
		return ""
	}
	return "\nYou may ask someone else to take the next round by ending your reply with a ```hand-on block holding {\"why\":\"your reason\"}. A hand-on accompanying a design question is ignored; ask again when this round finishes. Another free seat is preferred; if none is free you may take it again.\n"
}
