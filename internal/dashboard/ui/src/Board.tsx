import { requestHref } from "./router";
import { pmLandingLine } from "./landing";
import { latestFirst, projectTasks, requestStep } from "./stages";
import { boardColumns, doneLabel, readyLabel } from "./boardLanes";
import { AskForm } from "./AskForm";
import { CardList } from "./BoardCard";
import { TodoQueue } from "./TodoQueue";
import type { Project, Stage, State, Task } from "./api";

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
  const cards = (list: Task[]) => <CardList tasks={list} state={state} />;
  return (
    <div className="board-page">
      <AskForm project={project} refresh={refresh} />
      {ready.length > 0 && (
        <section className="board-ready" aria-label={readyLabel(project)}>
          <LaneTitle label={readyLabel(project)} count={ready.length} />
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
                    <LaneTitle label={lane.label} count={list.length} />
                    {lane.stage === "todo" ? (
                      <TodoQueue
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
      {done.length > 0 && <Landed project={project} tasks={done} />}
      {stopped.length > 0 && <Stopped tasks={stopped} />}
    </div>
  );
}

function LaneTitle({ label, count }: { label: string; count: number }) {
  return (
    <h2 className="board-lane-title">
      {label}
      <span className="count">{count}</span>
    </h2>
  );
}

function Landed({ project, tasks }: { project: Project; tasks: Task[] }) {
  return (
    <details className="disclosure landed">
      <summary>
        {doneLabel(project)} ({tasks.length})
      </summary>
      <ul className="card rows">
        {tasks.map((t) => {
          const ref = t.revisions?.at(-1)?.ref;
          return (
            <li key={t.id}>
              <a className="landed-row" href={requestHref(t.project_id, t.id)}>
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
  );
}

function Stopped({ tasks }: { tasks: Task[] }) {
  return (
    <details className="disclosure stopped">
      <summary>Stopped ({tasks.length})</summary>
      <ul className="card rows">
        {tasks.map((t) => (
          <li key={t.id}>
            <a className="stopped-row" href={requestHref(t.project_id, t.id)}>
              {t.objective}
              {t.detail && <span className="muted small">{t.detail}</span>}
            </a>
          </li>
        ))}
      </ul>
    </details>
  );
}
