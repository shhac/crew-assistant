import { useEffect, useRef, useState } from "react";
import { api, errorText, type ChatTurn } from "./api";
import { Icon } from "./ui";

/** Only the fields the queue works with, taken from the turn itself. */
type QueuedTurn = Pick<ChatTurn, "id" | "message" | "revision">;

export interface QueueHold {
  turn_id: string;
  reason: string;
  expires_at: string;
}

/** Moves one entry within an order, or returns the order unchanged. */
export function moveItem(ids: string[], from: number, to: number): string[] {
  if (
    from < 0 ||
    to < 0 ||
    from >= ids.length ||
    to >= ids.length ||
    from === to
  )
    return ids;
  const next = [...ids];
  next.splice(to, 0, ...next.splice(from, 1));
  return next;
}

/** Refreshed well inside the daemon's two-minute lease. */
const HEARTBEAT_MS = 30_000;

/**
 * The queue of messages waiting to run, and the controls for changing it.
 *
 * Changing a queued message holds the queue at that message: it and everything
 * behind it stay put while the owner works, so an order they never saw cannot
 * execute. The hold is the daemon's and lapses on its own, so closing this tab
 * mid-edit releases it rather than stranding the queue.
 *
 * Moving is available from the keyboard as well as by dragging, because a queue
 * that can only be reordered with a pointer is a queue some owners cannot
 * reorder at all.
 */
export function ChatQueue({
  turns,
  revision,
  hold,
  running,
  cancelling,
  onCancel,
  onChanged,
}: {
  turns: QueuedTurn[];
  revision: number;
  hold?: QueueHold | null;
  /** A reply is in progress, so the owner can keep writing behind it. */
  running?: boolean;
  cancelling?: Set<string>;
  onCancel?: (id: string) => void;
  onChanged: () => Promise<void> | void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [order, setOrder] = useState<string[] | null>(null);
  const [holding, setHolding] = useState<{ id: string; reason: string } | null>(
    null,
  );
  const dragging = useRef<string | null>(null);
  const dropped = useRef(false);
  const held = useRef<string | null>(null);

  // Keyed on what is held, not on the queue array: the queue polls, so that
  // array is rebuilt every second or so, and depending on it would release and
  // retake the lease on every poll — leaving a window where the message being
  // changed could start.
  //
  // Only work that spans time takes a lease. Moving with the buttons commits
  // immediately against the queue revision, so the revision check already
  // guarantees the order the owner saw; a lease would buy nothing.
  const holdID = holding?.id;
  const holdReason = holding?.reason;
  useEffect(() => {
    if (!holdID || !holdReason) return;
    let stopped = false;
    const release = (id: string) =>
      void api(`/api/chat/messages/${encodeURIComponent(id)}/hold`, {
        method: "DELETE",
      }).catch(() => {
        /* The lease lapses on its own; a failed release is not the owner's problem. */
      });
    const take = async () => {
      try {
        await api(`/api/chat/messages/${encodeURIComponent(holdID)}/hold`, {
          method: "POST",
          body: JSON.stringify({ reason: holdReason }),
        });
        // The lease may have been taken after this effect was torn down. Left
        // alone it would hold the queue for its whole term with nobody
        // refreshing or releasing it.
        if (stopped) {
          release(holdID);
          return;
        }
        held.current = holdID;
      } catch (err) {
        if (!stopped) setError(errorText(err));
      }
    };
    void take();
    const timer = setInterval(() => void take(), HEARTBEAT_MS);
    return () => {
      stopped = true;
      clearInterval(timer);
      const outstanding = held.current;
      held.current = null;
      if (outstanding) release(outstanding);
    };
  }, [holdID, holdReason]);

  const shown = order
    ? order
        .map((id) => turns.find((t) => t.id === id))
        .filter((t): t is QueuedTurn => !!t)
    : turns;

  async function save(turn: QueuedTurn) {
    setBusy(true);
    setError("");
    try {
      await api(`/api/chat/messages/${encodeURIComponent(turn.id)}`, {
        method: "PATCH",
        body: JSON.stringify({ message: draft, revision: turn.revision }),
      });
      setEditing(null);
      setDraft("");
      setHolding(null);
      await onChanged();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  async function commit(next: string[]) {
    setBusy(true);
    setError("");
    try {
      await api("/api/chat/queue", {
        method: "PUT",
        body: JSON.stringify({ order: next, revision }),
      });
      setOrder(null);
      await onChanged();
    } catch (err) {
      setError(errorText(err));
      setOrder(null);
    } finally {
      setBusy(false);
    }
  }

  function move(id: string, by: number) {
    const current = shown.map((t) => t.id);
    const from = current.indexOf(id);
    const next = moveItem(current, from, from + by);
    if (next === current) return;
    setOrder(next);
    void commit(next);
  }

  if (!turns.length)
    return running ? (
      <p className="queue-hint">
        Keep writing if you like. Each message gets its own reply.
      </p>
    ) : null;
  return (
    <section className="queue" aria-label="Queued messages">
      <p className="label">Up next ({turns.length})</p>
      {hold && (
        <p className="queue-hint" role="status">
          Paused while you change it. It picks up again on its own.
        </p>
      )}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      <ol className="queue-list">
        {shown.map((turn, index) => (
          <li
            key={turn.id}
            className="queue-item"
            aria-label={`Queued message ${index + 1}`}
            draggable={editing === null && !busy}
            onDragStart={() => {
              dragging.current = turn.id;
              const current = shown.map((t) => t.id);
              setOrder(current);
              // A drag spans time, so it takes a lease. The move buttons do
              // not: they commit at once against the queue revision, which
              // already guarantees the order the owner saw.
              if (current[0])
                setHolding({ id: current[0], reason: "reordering" });
            }}
            onDragOver={(event) => {
              event.preventDefault();
              const from = dragging.current;
              if (!from || from === turn.id) return;
              const current = shown.map((t) => t.id);
              setOrder(
                moveItem(
                  current,
                  current.indexOf(from),
                  current.indexOf(turn.id),
                ),
              );
            }}
            onDrop={(event) => {
              event.preventDefault();
              dropped.current = true;
              if (order) void commit(order);
            }}
            onDragEnd={() => {
              dragging.current = null;
              setHolding(null);
              // A drag abandoned outside the list never drops. The owner did
              // not choose that order, so it is not committed.
              if (!dropped.current) setOrder(null);
              dropped.current = false;
            }}
          >
            <span className="queue-position" aria-hidden="true">
              {index + 1}
            </span>
            {editing === turn.id ? (
              <div className="queue-editor">
                <label className="control" htmlFor={`queue-edit-${turn.id}`}>
                  Edit message
                  <textarea
                    id={`queue-edit-${turn.id}`}
                    className="field"
                    rows={3}
                    maxLength={24000}
                    value={draft}
                    onChange={(event) => setDraft(event.target.value)}
                  />
                </label>
                <div className="actions">
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    disabled={busy || !draft.trim()}
                    onClick={() => void save(turn)}
                  >
                    Save
                  </button>
                  <button
                    type="button"
                    className="btn btn-quiet btn-sm"
                    onClick={() => {
                      setEditing(null);
                      setDraft("");
                      setHolding(null);
                    }}
                  >
                    Cancel
                  </button>
                </div>
              </div>
            ) : (
              <>
                <p className="queue-text">{turn.message}</p>
                <div className="queue-actions">
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
                    aria-label={`Move message ${index + 1} earlier`}
                    disabled={busy || index === 0}
                    onClick={() => move(turn.id, -1)}
                  >
                    <Icon name="Up" size={13} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
                    aria-label={`Move message ${index + 1} later`}
                    disabled={busy || index === shown.length - 1}
                    onClick={() => move(turn.id, 1)}
                  >
                    <Icon name="Down" size={13} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-quiet btn-sm"
                    aria-label={`Edit message ${index + 1}`}
                    disabled={busy}
                    onClick={() => {
                      setEditing(turn.id);
                      setDraft(turn.message);
                      setHolding({ id: turn.id, reason: "editing" });
                    }}
                  >
                    Edit
                  </button>
                  {onCancel && (
                    <button
                      type="button"
                      className="btn btn-quiet btn-sm"
                      aria-label={`Remove queued message: ${turn.message}`}
                      disabled={busy || cancelling?.has(turn.id)}
                      onClick={() => onCancel(turn.id)}
                    >
                      Remove
                    </button>
                  )}
                </div>
              </>
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}
