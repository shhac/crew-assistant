import {
  useState,
  type DragEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import { projectHref, requestHref } from "./router";
import { pmLandingLine } from "./landing";
import { atWork } from "./members";
import {
  boardColumns,
  decisionFor,
  doneLabel,
  isOpenMessage,
  latestFirst,
  needsYou,
  orderLine,
  projectTasks,
  readyLabel,
  requestStep,
} from "./stages";
import { Avatar } from "./Avatar";
import { ErrorNotice, Icon, Pill, useAction } from "./ui";
import {
  askForTask,
  criteriaLines,
  orderTasks,
  type Decision,
  type Member,
  type Project,
  type Stage,
  type State,
  type Task,
} from "./api";

/** What a queued request waits to land first, or "" when it waits for nothing. */
const waitsLine = (task: Task) =>
  task.waits_for?.length
    ? `Waits for ${task.waits_for.map((w) => `“${w}”`).join(", ")}`
    : "";

export function Board({
  project,
  state,
  refresh,
}: {
  project: Project;
  state: State;
  refresh: () => Promise<void>;
}) {
  // The server's order is the order work starts in; keep it.
  const tasks = projectTasks(project, state.tasks);
  const columns = boardColumns(project, tasks);
  const at = (stage: Stage) => tasks.filter((t) => t.stage === stage);
  const ready = at("ready");
  const done = latestFirst(at("done"));
  const stopped = at("stopped");
  const cards = (list: Task[]) => (
    <CardList
      tasks={list}
      decisionFor={(t) => decisionFor(t, state.decisions)}
      members={state.members}
    />
  );
  return (
    <div className="board-page">
      <AskForm project={project} refresh={refresh} />
      {ready.length > 0 && (
        <section className="board-ready" aria-label={readyLabel(project)}>
          <h2 className="board-lane-title">
            {readyLabel(project)}
            <span className="count">{ready.length}</span>
          </h2>
          {cards(ready)}
        </section>
      )}
      {tasks.length === 0 ? (
        <p className="muted">Nothing asked for yet.</p>
      ) : (
        <div className="board" role="list" aria-label="Board">
          {columns.map((column) => (
            <div key={column.key} className="board-column">
              {column.lanes.map((lane) => {
                const list = at(lane.stage);
                return (
                  <section
                    key={lane.stage}
                    className={`board-lane${list.length ? "" : " quiet"}`}
                    role="listitem"
                    aria-label={lane.label}
                  >
                    <h2 className="board-lane-title">
                      {lane.label}
                      <span className="count">{list.length}</span>
                    </h2>
                    {lane.stage === "todo" ? (
                      <TodoColumn
                        tasks={list}
                        project={project}
                        refresh={refresh}
                      />
                    ) : (
                      cards(list)
                    )}
                  </section>
                );
              })}
            </div>
          ))}
        </div>
      )}
      {done.length > 0 && (
        <details className="disclosure landed">
          <summary>
            {doneLabel(project)} ({done.length})
          </summary>
          <ul className="card rows">
            {done.map((t) => {
              const ref = t.revisions?.at(-1)?.ref;
              return (
                <li key={t.id}>
                  <a
                    className="landed-row"
                    href={requestHref(t.project_id, t.id)}
                  >
                    {t.objective}
                    <span className="muted small">
                      {requestStep(t)}
                      {pmLandingLine(t) && ` · ${pmLandingLine(t)}`}
                      {ref && (
                        <>
                          {" · "}
                          <code>{ref.slice(0, 7)}</code>
                        </>
                      )}
                    </span>
                  </a>
                </li>
              );
            })}
          </ul>
        </details>
      )}
      {stopped.length > 0 && (
        <details className="disclosure stopped">
          <summary>Stopped ({stopped.length})</summary>
          <ul className="card rows">
            {stopped.map((t) => (
              <li key={t.id}>
                <a
                  className="stopped-row"
                  href={requestHref(t.project_id, t.id)}
                >
                  {t.objective}
                  {t.detail && <span className="muted small">{t.detail}</span>}
                </a>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}

function CardList({
  tasks,
  decisionFor,
  members,
}: {
  tasks: Task[];
  decisionFor: (t: Task) => Decision | undefined;
  members: Member[];
}) {
  return (
    <ul className="board-cards">
      {tasks.map((t) => (
        <li key={t.id}>
          <BoardCard
            task={t}
            decision={decisionFor(t)}
            worker={atWork(t, members)}
          />
        </li>
      ))}
    </ul>
  );
}

function BoardCard({
  task,
  decision,
  worker,
  children,
}: {
  task: Task;
  decision?: Decision;
  /** The member at work on it now, if a member is. */
  worker?: Member;
  children?: ReactNode;
}) {
  const open = (task.messages ?? []).filter(isOpenMessage).length;
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
      {pm && <p className="board-card-meta muted small">{pm}</p>}
      {open > 0 && (
        <p className="board-card-meta muted small">
          <span>
            <Icon name="Message" size={12} /> {open} waiting for a reply
          </span>
        </p>
      )}
      {children}
    </article>
  );
}

/**
 * The to-do list in the order work starts in. The owner can reorder it by
 * dragging, or with the move buttons from the keyboard; so can the assistant.
 */
function TodoColumn({
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

function AskForm({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [objective, setObjective] = useState("");
  const [criteria, setCriteria] = useState("");
  const [judging, setJudging] = useState(false);
  const { busy, error, run } = useAction();
  if (!project.brief.goal)
    return (
      <p className="ask-blocked card">
        Needs a brief first.{" "}
        <a href={projectHref(project.id, "brief")}>Write the brief</a>
      </p>
    );
  if (!project.playbook)
    return (
      <p className="ask-blocked card">
        Needs a team first.{" "}
        <a href={projectHref(project.id, "team")}>Choose a team</a>
      </p>
    );
  async function ask(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await askForTask(project.id, {
        objective: objective.trim(),
        criteria: criteriaLines(criteria),
      });
      setObjective("");
      setCriteria("");
      setJudging(false);
      await refresh();
    });
  }
  return (
    <form className="ask card" onSubmit={ask} aria-label="Ask the team">
      <div className="ask-row">
        <label className="sr-only" htmlFor="ask-objective">
          What do you want?
        </label>
        <input
          id="ask-objective"
          className="field"
          value={objective}
          onChange={(e) => setObjective(e.target.value)}
          placeholder="Ask the team for something"
          maxLength={20000}
          required
        />
        <button
          className="btn btn-primary"
          type="submit"
          disabled={busy || !objective.trim()}
        >
          Ask
        </button>
      </div>
      {judging ? (
        <label className="control">
          How you'll judge it, one point per line
          <textarea
            className="field"
            value={criteria}
            onChange={(e) => setCriteria(e.target.value)}
            rows={2}
            maxLength={20000}
          />
        </label>
      ) : (
        <button
          type="button"
          className="link-button small ask-more"
          onClick={() => setJudging(true)}
        >
          + Say how you'll judge it
        </button>
      )}
      <ErrorNotice error={error} />
    </form>
  );
}
