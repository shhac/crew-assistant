import type { ReactNode } from "react";
import { requestHref } from "./router";
import { pmLandingLine } from "./landing";
import { atWork, memberOf, workingSeats } from "./members";
import { decisionFor, isOpenMessage, needsYou, requestStep } from "./stages";
import { TaskActivity } from "./TaskActivity";
import { Avatar } from "./Avatar";
import { Icon, Pill, TaskRef } from "./ui";
import type { Decision, Member, Project, State, Task, Turn } from "./api";

/**
 * The members at work on a request now, one for each seat at work, or the
 * one about to pick it up when no seat is.
 */
function workers(task: Task, state: State) {
  const seats = workingSeats(task, state.turns);
  if (!seats.length) {
    const next = atWork(task, state.members);
    return next ? [next] : [];
  }
  return seats.flatMap((r) => {
    const member = memberOf(r, state.members);
    return member ? [member] : [];
  });
}

export function CardList({
  tasks,
  state,
  project,
}: {
  tasks: Task[];
  state: State;
  project?: Project;
}) {
  return (
    <ul className="board-cards">
      {tasks.map((t) => (
        <li key={t.id}>
          <BoardCard
            task={t}
            decision={decisionFor(t, state.decisions)}
            workers={workers(t, state)}
            turns={state.turns}
            project={project}
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
  workers = [],
  turns,
  project,
  activity,
  children,
}: {
  task: Task;
  decision?: Decision;
  /** The members at work on it now, such as a reviewer and QA side by side. */
  workers?: Member[];
  /** The turns running now, when known, so the step says who is at work. */
  turns?: Turn[];
  /** The request's project, which names the stages of one not yet started. */
  project?: Project;
  activity?: ReactNode;
  children?: ReactNode;
}) {
  const open = (task.messages ?? []).filter(isOpenMessage).length;
  const blocks = task.blocks?.length ?? 0;
  const pm = pmLandingLine(task);
  return (
    <article
      className={`board-card${needsYou(task) ? " needs" : ""}${task.stage === "triage" ? " triage" : ""}`}
    >
      <TaskRef task={task} />
      <a
        className="board-card-link"
        href={requestHref(task.project_id, task.id)}
      >
        {task.objective}
      </a>
      {/* A to-do card says only who or what its start waits for. */}
      {(task.stage !== "todo" || task.waiting) && (
        <p className="board-card-step">
          {workers.map((m, i) => (
            <Avatar key={`${m.id}.${i}`} of={m} size={20} />
          ))}
          {needsYou(task) ? (
            <Pill tone="needs" dot>
              {requestStep(task, decision, turns, project)}
            </Pill>
          ) : (
            requestStep(task, decision, turns, project)
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
