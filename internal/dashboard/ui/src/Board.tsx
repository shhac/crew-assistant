import { projectHref, requestHref } from "./router";
import { landingPausedLine, pmLandingLine } from "./landing";
import {
  capLine,
  finished,
  latestFirst,
  projectTasks,
  requestStep,
  stageLimit,
} from "./stages";
import { boardColumns, boardRows, doneLabel } from "./boardLanes";
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
  const rows = boardRows(project, tasks);
  const done = latestFirst(at("done"));
  const stopped = at("stopped");
  const cards = (list: Task[]) => (
    <CardList tasks={list} state={state} project={project} />
  );
  // A stage with a limit counts the requests it holds, including those done
  // with it and waiting for room in the next. Backward handoffs display
  // immediately; checks can hold Ready before their displayed lane moves.
  const holds = (stage: Stage) =>
    tasks.filter(
      (t) =>
        !finished(t) &&
        t.status !== "triage" &&
        (stage === "todo" ? t.status === "queued" : t.status !== "queued") &&
        (t.status === "reviewing" || t.status === "deciding"
          ? t.place || t.stage
          : t.stage) === stage,
    ).length;
  const title = (stage: Stage, label: string, count: number) => {
    const limit = stageLimit(project.playbook, stage);
    return (
      <LaneTitle
        label={label}
        count={
          limit
            ? `${holds(stage)} of ${limit}`
            : String(stage === "triage" ? count : holds(stage))
        }
      />
    );
  };
  const cap = capLine(project, state.tasks);
  return (
    <div className="board-page">
      <AskForm project={project} refresh={refresh} />
      {project.landing_paused && (
        <p className="board-cap muted small">
          {landingPausedLine(project)} ·{" "}
          <a href={projectHref(project.id, "config")}>Landing</a>
        </p>
      )}
      {rows.map(({ stage, label }) => {
        const list = at(stage);
        // Empty rows stay hidden unless the owner set their capacity.
        if (
          !list.length &&
          !holds(stage) &&
          !(project.playbook?.stage_limits?.[stage] ?? 0)
        )
          return null;
        return (
          <section key={stage} className="board-ready" aria-label={label}>
            {title(stage, label, list.length)}
            {cards(list)}
          </section>
        );
      })}
      {tasks.length > 0 && cap && (
        <p className="board-cap muted small">
          {cap} ·{" "}
          <a href={projectHref(project.id, "config")}>Column capacity</a>
        </p>
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
                    {title(lane.stage, lane.label, list.length)}
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

function LaneTitle({ label, count }: { label: string; count: string }) {
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
