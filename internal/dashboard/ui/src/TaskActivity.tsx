import { isQuiet, turnFor, waitingLine, workingParts } from "./turns";
import type { State, Task } from "./api";

/**
 * Whether a role is at work on a request right now, and how it is getting
 * on, or who has yet to pick it up and why. It is worked out afresh each
 * time the state is polled, so the times move with it.
 */
export function TaskActivity({
  task,
  state,
  full = false,
}: {
  task: Task;
  state: State;
  /** The request panel says more than a board card has room for. */
  full?: boolean;
}) {
  const now = Date.now();
  const turn = turnFor(task, state.turns);
  if (turn)
    return (
      <p
        className={`task-activity working${isQuiet(turn, now) ? " quiet" : ""}`}
      >
        <span className="dot" aria-hidden="true" />
        <span>
          {workingParts(turn, now, !full).join(" · ")}
          {full && turn.tool && (
            <>
              {" · now: "}
              <code>{turn.tool}</code>
            </>
          )}
        </span>
      </p>
    );
  const waiting = waitingLine(task, state, now);
  if (!waiting) return null;
  return <p className="task-activity waiting">{waiting}</p>;
}
