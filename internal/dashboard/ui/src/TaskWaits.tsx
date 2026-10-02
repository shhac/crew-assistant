import { Fragment } from "react";
import type { Blocker, Task } from "./api";
import { requestHref } from "./router";

export const prerequisiteWait = (b: Blocker) =>
  b.answer_pending
    ? `Waits for your answer: is ${b.description} ready?`
    : `Waits for you to clear “${b.description}” on the task page`;

export const hasWaits = (task: Task) =>
  !!(
    task.waiting_on?.length ||
    task.waits_for?.length ||
    task.blockers?.some((b) => !b.cleared_at)
  );

/** The same waiting words and links in the queue and on a board card. */
export function TaskWaits({
  task,
  card = false,
}: {
  task: Task;
  card?: boolean;
}) {
  const waits = task.waiting_on ?? [];
  const parts = [
    ...waits.map((w) => (
      <Fragment key={w.task}>
        Waits for{" "}
        <a href={requestHref(w.project_id, w.task)}>
          {w.ref} “{w.objective}”
        </a>
        {w.project_id !== task.project_id && <> in {w.project}</>}
      </Fragment>
    )),
    ...(!waits.length && task.waits_for?.length
      ? [
          <Fragment key="objectives">
            Waits for {task.waits_for.map((w) => `“${w}”`).join(", ")}
          </Fragment>,
        ]
      : []),
    ...(task.blockers ?? [])
      .filter((b) => !b.cleared_at)
      .map((b) => (
        <Fragment key={b.id}>
          {b.kind === "prerequisite"
            ? prerequisiteWait(b)
            : `Held until: ${b.description}${b.landing_only ? " (landing only)" : ""}`}
        </Fragment>
      )),
  ];
  if (card)
    return (
      <>
        {parts.map((part, i) => (
          <p key={i} className="board-card-meta muted small">
            {part}
          </p>
        ))}
      </>
    );
  return (
    <>
      {parts.map((part, i) => (
        <Fragment key={i}>
          {i > 0 && "; "}
          {part}
        </Fragment>
      ))}
    </>
  );
}
