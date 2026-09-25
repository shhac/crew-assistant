import { useState, type DragEvent } from "react";
import { orderLine } from "./stages";
import { BoardCard } from "./BoardCard";
import { ErrorNotice, Icon, useAction } from "./ui";
import { orderTasks, type Project, type Task } from "./api";

/** What a queued request waits to land first, or "" when it waits for nothing. */
const waitsLine = (task: Task) =>
  task.waits_for?.length
    ? `Waits for ${task.waits_for.map((w) => `“${w}”`).join(", ")}`
    : "";

/**
 * The to-do list in the order work starts in. The owner can reorder it by
 * dragging, or with the move buttons from the keyboard; so can the assistant.
 */
export function TodoQueue({
  tasks,
  project,
  refresh,
}: {
  tasks: Task[];
  project: Project;
  refresh: () => Promise<void>;
}) {
  const { busy, error, run } = useAction();
  const [dragging, setDragging] = useState("");
  const reorder = (ids: string[]) =>
    run(async () => {
      // A refused order was usually made from a stale list; fetch the
      // current one either way so the next try starts from it.
      try {
        await orderTasks(project.id, ids);
      } finally {
        await refresh();
      }
    });
  const ids = tasks.map((t) => t.id);
  const move = (from: number, to: number) => {
    if (to < 0 || to >= ids.length || from === to) return;
    const next = [...ids];
    const [id] = next.splice(from, 1);
    next.splice(to, 0, id);
    void reorder(next);
  };
  const drop = (event: DragEvent, to: number) => {
    event.preventDefault();
    const from = ids.indexOf(dragging);
    setDragging("");
    if (from >= 0) move(from, to);
  };
  const ordered = orderLine(project, tasks.length);
  return (
    <>
      {ordered && <p className="todo-order muted small">{ordered}</p>}
      <ol className="board-cards todo">
        {tasks.map((t, i) => (
          <li
            key={t.id}
            draggable={!busy}
            className={dragging === t.id ? "dragging" : undefined}
            onDragStart={(e) => {
              setDragging(t.id);
              e.dataTransfer.effectAllowed = "move";
            }}
            onDragEnd={() => setDragging("")}
            onDragOver={(e) => dragging && e.preventDefault()}
            onDrop={(e) => drop(e, i)}
          >
            <BoardCard task={t}>
              <Place
                task={t}
                index={i}
                count={tasks.length}
                busy={busy}
                move={move}
              />
            </BoardCard>
          </li>
        ))}
      </ol>
      <ErrorNotice error={error} />
    </>
  );
}

/**
 * Where a queued request stands: what it waits for, or else its place in
 * line, with the buttons that move it.
 */
function Place({
  task,
  index,
  count,
  busy,
  move,
}: {
  task: Task;
  index: number;
  count: number;
  busy: boolean;
  move: (from: number, to: number) => void;
}) {
  const place = index === 0 ? "Next" : `#${index + 1}`;
  const hint = waitsLine(task) || (count > 1 ? place : "");
  if (!hint) return null;
  return (
    <div className="reorder">
      <span className="muted small">{hint}</span>
      {count > 1 && (
        <>
          <button
            type="button"
            className="btn btn-quiet btn-icon btn-sm"
            aria-label={`Move “${task.objective}” up`}
            disabled={busy || index === 0}
            onClick={() => move(index, index - 1)}
          >
            <Icon name="Up" size={14} />
          </button>
          <button
            type="button"
            className="btn btn-quiet btn-icon btn-sm"
            aria-label={`Move “${task.objective}” down`}
            disabled={busy || index === count - 1}
            onClick={() => move(index, index + 1)}
          >
            <Icon name="Down" size={14} />
          </button>
        </>
      )}
    </div>
  );
}
