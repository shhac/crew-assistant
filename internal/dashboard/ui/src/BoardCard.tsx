import type { ReactNode } from "react";
import { requestHref } from "./router";
import { pmLandingLine } from "./landing";
import { atWork } from "./members";
import { decisionFor, isOpenMessage, needsYou, requestStep } from "./stages";
import { TaskActivity } from "./TaskActivity";
import { Avatar } from "./Avatar";
import { Icon, Pill } from "./ui";
import type { Decision, Member, State, Task } from "./api";

export function CardList({ tasks, state }: { tasks: Task[]; state: State }) {
  return (
    <ul className="board-cards">
      {tasks.map((t) => (
        <li key={t.id}>
          <BoardCard
            task={t}
            decision={decisionFor(t, state.decisions)}
            worker={atWork(t, state.members)}
            activity={<TaskActivity task={t} state={state} />}
          />
        </li>
      ))}
    </ul>
  );
}

export function BoardCard({
  task,
  decision,
  worker,
  activity,
  children,
}: {
  task: Task;
  decision?: Decision;
  /** The member at work on it now, if a member is. */
  worker?: Member;
  activity?: ReactNode;
  children?: ReactNode;
}) {
  const open = (task.messages ?? []).filter(isOpenMessage).length;
  const blocks = task.blocks?.length ?? 0;
  const pm = pmLandingLine(task);
  return (
    <article className={`board-card${needsYou(task) ? " needs" : ""}`}>
      <a
        className="board-card-link"
        href={requestHref(task.project_id, task.id)}
      >
        {task.objective}
      </a>
      {task.stage !== "todo" && (
        <p className="board-card-step">
          {worker && <Avatar of={worker} size={20} />}
          {needsYou(task) ? (
            <Pill tone="needs" dot>
              {requestStep(task, decision)}
            </Pill>
          ) : (
            requestStep(task, decision)
          )}
        </p>
      )}
      {activity}
      {pm && <p className="board-card-meta muted small">{pm}</p>}
      {(open > 0 || blocks > 0) && (
        <p className="board-card-meta muted small">
          {open > 0 && (
            <span>
              <Icon name="Message" size={12} /> {open} waiting for a reply
            </span>
          )}
          {blocks > 0 && <span>Blocks {blocks}</span>}
        </p>
      )}
      {children}
    </article>
  );
}
