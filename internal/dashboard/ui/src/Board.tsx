import {
  useState,
  type DragEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import { projectHref, requestHref } from "./router";
import {
  boardColumns,
  decisionFor,
  isOpenMessage,
  needsYou,
  projectTasks,
  requestStep,
} from "./stages";
import { ErrorNotice, Icon, Pill, useAction } from "./ui";
import {
  askForTask,
  criteriaLines,
  orderTasks,
  type Decision,
  type Project,
  type State,
  type Task,
} from "./api";

const shownDone = 6;

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
  const stopped = tasks.filter((t) => t.stage === "stopped");
  return (
    <div className="board-page">
      <AskForm project={project} refresh={refresh} />
      {tasks.length === 0 ? (
        <p className="muted">Nothing asked for yet.</p>
      ) : (
        <div className="board" role="list" aria-label="Board">
          {columns.map((column) => {
            const cards = tasks.filter((t) => t.stage === column.stage);
            return (
              <section
                key={column.stage}
                className={`board-column${cards.length ? "" : " empty"}`}
                role="listitem"
                aria-label={column.label}
              >
                <h2 className="board-column-title">
                  {column.label}
                  <span className="count">{cards.length}</span>
                </h2>
                {column.stage === "todo" ? (
                  <TodoColumn
                    tasks={cards}
                    project={project}
                    refresh={refresh}
                  />
                ) : (
                  <CardList
                    tasks={
                      column.stage === "done" ? [...cards].reverse() : cards
                    }
                    limit={column.stage === "done" ? shownDone : undefined}
                    decisionFor={(t) => decisionFor(t, state.decisions)}
                  />
                )}
              </section>
            );
          })}
        </div>
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
  limit,
  decisionFor,
}: {
  tasks: Task[];
  limit?: number;
  decisionFor: (t: Task) => Decision | undefined;
}) {
  const [all, setAll] = useState(false);
  const shown = limit && !all ? tasks.slice(0, limit) : tasks;
  return (
    <ul className="board-cards">
      {shown.map((t) => (
        <li key={t.id}>
          <BoardCard task={t} decision={decisionFor(t)} />
        </li>
      ))}
      {limit && tasks.length > limit && !all && (
        <li>
          <button className="link-button small" onClick={() => setAll(true)}>
            Show all {tasks.length}
          </button>
        </li>
      )}
    </ul>
  );
}

function BoardCard({
  task,
  decision,
  children,
}: {
  task: Task;
  decision?: Decision;
  children?: ReactNode;
}) {
  const open = (task.messages ?? []).filter(isOpenMessage).length;
  const ref = task.revisions?.at(-1)?.ref;
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
          {needsYou(task) ? (
            <Pill tone="needs" dot>
              {requestStep(task, decision)}
            </Pill>
          ) : (
            requestStep(task, decision)
          )}
        </p>
      )}
      {(open > 0 || (task.stage === "done" && ref)) && (
        <p className="board-card-meta muted small">
          {open > 0 && (
            <span>
              <Icon name="Message" size={12} /> {open} waiting for a reply
            </span>
          )}
          {task.stage === "done" && ref && <code>{ref.slice(0, 7)}</code>}
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
  return (
    <>
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
              {tasks.length > 1 && (
                <div className="reorder">
                  <span className="muted small">
                    {i === 0 ? "Next" : `#${i + 1}`}
                  </span>
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
                    aria-label={`Move “${t.objective}” up`}
                    disabled={busy || i === 0}
                    onClick={() => move(i, i - 1)}
                  >
                    <Icon name="Up" size={14} />
                  </button>
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
                    aria-label={`Move “${t.objective}” down`}
                    disabled={busy || i === tasks.length - 1}
                    onClick={() => move(i, i + 1)}
                  >
                    <Icon name="Down" size={14} />
                  </button>
                </div>
              )}
            </BoardCard>
          </li>
        ))}
      </ol>
      <ErrorNotice error={error} />
    </>
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
