import { fullDateLabel } from "./ui";
import { ToolActivity } from "./ToolActivity";
import { delivery, type VisibleTurn } from "./chatTurns";

/**
 * What happened to one owner message: its delivery state, any recovery the
 * owner can choose, the assistant's tool activity and its working indicator.
 * Rendered for a message that has a turn; absent when it has none.
 */
export function TurnStatus({
  turn,
  live,
  name,
  cancelling,
  onCancel,
  onRestore,
  onDiscard,
  onRetry,
}: {
  turn?: VisibleTurn;
  /** Nothing in the thread is newer than this turn, and it has not replied. */
  live?: boolean;
  name: string;
  cancelling: Set<string>;
  onCancel: (turn: VisibleTurn) => void;
  onRestore: (turn: VisibleTurn) => void;
  onDiscard: (turn: VisibleTurn) => void;
  onRetry: (turn: VisibleTurn) => void;
}) {
  if (!turn) return null;
  const said = delivery(turn);
  const retry = fullDateLabel(turn.retry_at);
  return (
    <div className="turn-status">
      {said && (
        <p className="turn-delivery">
          {said}
          {turn.status === "waiting" && (
            <button
              type="button"
              className="link-button"
              disabled={cancelling.has(turn.id)}
              onClick={() => void onCancel(turn)}
              aria-label={`Cancel message: ${turn.message}`}
            >
              Cancel
            </button>
          )}
        </p>
      )}
      {turn.browser_note && <p className="muted small">{turn.browser_note}</p>}
      {turn.error && (
        <p className="error" role="alert">
          {turn.error}
        </p>
      )}
      {turn.status === "unconfirmed" && (
        <div className="turn-recovery">
          <p className="muted small">
            Retrying is safe: it can't start a second reply.
          </p>
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => void onRetry(turn)}
          >
            Retry
          </button>
        </div>
      )}
      {turn.status === "rejected" && (
        <div className="turn-recovery actions">
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => onRestore(turn)}
          >
            Edit
          </button>
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => onDiscard(turn)}
          >
            Discard
          </button>
        </div>
      )}
      {!!turn.events?.length && (
        <ToolActivity events={turn.events} live={live} />
      )}
      {turn.status === "running" && (
        <p className="turn-working" role="status">
          <span className="dot" aria-hidden="true" />
          {turn.model_status || turn.loading_phrase || `${name} is working`}
          {retry && <span className="muted small"> · retrying at {retry}</span>}
        </p>
      )}
    </div>
  );
}
