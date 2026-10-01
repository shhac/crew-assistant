package work

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shhac/crew-assistant/internal/core"
)

// Receipts use the resulting record, never model-authored summaries.
// call holds chat.mu, also serializing the per-turn queue bound.
func (r roleTools) recordChatChange(ctx context.Context, name string, raw json.RawMessage, _ string) {
	var in map[string]string
	_ = json.Unmarshal(raw, &in)
	snap, err := r.lp.Core.Snapshot(ctx)
	if err != nil {
		return
	}
	change := core.PMChatChange{Kind: "updated"}
	add := func(id string) string {
		if t, ok := findTask(snap, r.projectID, id); ok {
			change.Tasks = append(change.Tasks, t.ID)
			return t.Objective
		}
		return "request"
	}
	switch name {
	case "order_tasks", "queue_task":
		// These tools record their exact committed result directly.
		return
	case "link_tasks":
		change.Kind = "linked"
		a, b := add(in["task_id"]), add(in["other_task_id"])
		switch in["relation"] {
		case core.RelationDependsOn:
			change.Summary = a + " now waits for " + b
		case core.RelationBlocks:
			change.Summary = b + " now waits for " + a
		default:
			change.Summary = "Linked “" + a + "” and “" + b + "”"
		}
	case "edit_task", "set_blocker", "clear_blocker", "add_note", "unlink_tasks":
		title := add(in["task_id"])
		change.Summary = "Updated “" + title + "”"
		if name == "add_note" {
			change.Summary = "Added a note to “" + title + "”"
		}
	case "propose_run_recipe":
		change.Summary = "Proposed how QA runs this project; awaiting your decision"
	default:
		return
	}
	r.chat.items = append(r.chat.items, change)
}

func orderedReceipt(tasks []core.Task) core.PMChatChange {
	change := core.PMChatChange{Kind: "reordered", Tasks: []string{}}
	var titles []string
	for i, t := range tasks {
		change.Tasks = append(change.Tasks, t.ID)
		if i < 8 {
			titles = append(titles, t.Objective)
		}
	}
	change.Summary = "Reordered to-do list: " + strings.Join(titles, " → ")
	if len(tasks) > 8 {
		change.Summary += fmt.Sprintf(" → … (%d more)", len(tasks)-8)
	}
	return change
}
