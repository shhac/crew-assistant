package core

import "time"

// wakeTurnMessage writes the report a wake-up turn delivers, stamping each
// wake with the moment it is delivered.
func wakeTurnMessage(v *Snapshot, ids []string, now time.Time) string {
	var wakes []Wake
	for _, id := range ids {
		for i := range v.Wakes {
			if w := &v.Wakes[i]; w.ID == id && w.Status == WakeFired {
				w.DeliveredAt = &now
				wakes = append(wakes, *w)
			}
		}
	}
	if len(wakes) == 0 {
		return "[Wake-up from the daemon, not a message from the owner] The wake-ups this turn was for were cancelled. Nothing to do."
	}
	return "[Wake-up from the daemon, not a message from the owner. You asked to be woken; act on your continuation if it still applies, and tell the owner only what they need to know.]\n\n" + WakeReport(wakes, now)
}

// maxWakeAttempts bounds how often a wake is offered again after the turn
// carrying it failed, so a model that keeps failing cannot loop forever.
const maxWakeAttempts = 3

// settleWakeTurn finishes the wakes a wake-up turn carried. They count as
// delivered only when the assistant completed the turn; otherwise they are
// offered again in a later turn, unless the owner cancelled it.
func settleWakeTurn(v *Snapshot, t *ChatTurn, status string, now time.Time) {
	for _, id := range t.WakeIDs {
		for i := range v.Wakes {
			w := &v.Wakes[i]
			if w.ID != id || w.Status != WakeFired {
				continue
			}
			switch {
			case status == "completed":
				w.Status = WakeDelivered
			case status == "cancelled":
				w.Status = WakeCancelled
			case w.Attempts+1 >= maxWakeAttempts:
				w.Attempts++
				w.Status = WakeDelivered
			default:
				w.Attempts++
				w.DeliveredAt = nil
				queueWakeTurn(v, w.ID, now)
			}
		}
	}
}

// queueWakeTurn adds the wake to the assistant's queued wake-up turn, so
// several that fire while it is busy arrive together, each with its own times.
func queueWakeTurn(v *Snapshot, id string, now time.Time) {
	for i := range v.ChatTurns {
		t := &v.ChatTurns[i]
		if t.Status == "queued" && t.Origin == OriginWake {
			t.WakeIDs = append(t.WakeIDs, id)
			return
		}
	}
	v.ChatTurns = append(v.ChatTurns, ChatTurn{ID: uid(), Message: "Wake-up", Status: "queued", CreatedAt: now, Origin: OriginWake, WakeIDs: []string{id}})
	v.ChatQueueRevision++
}

func removeFromWakeTurns(v *Snapshot, id string, now time.Time) {
	for i := range v.ChatTurns {
		t := &v.ChatTurns[i]
		if t.Status != "queued" || t.Origin != OriginWake {
			continue
		}
		kept := t.WakeIDs[:0]
		for _, w := range t.WakeIDs {
			if w != id {
				kept = append(kept, w)
			}
		}
		t.WakeIDs = kept
		if len(kept) == 0 {
			t.Status, t.FinishedAt = "cancelled", &now
			v.ChatQueueRevision++
		}
	}
}
