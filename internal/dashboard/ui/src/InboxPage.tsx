import { useState, type FormEvent } from "react";
import { DecisionCard } from "./DecisionCard";
import { projectHref, requestHref } from "./router";
import { finished, requestStep, requestTone } from "./stages";
import { ErrorNotice, Pill, dateLabel, recordedTime, sinceLabel } from "./ui";
import {
  api,
  errorText,
  pendingDecisions,
  type Decision,
  type PendingOperation,
  type Project,
  type State,
  type Task,
} from "./api";

const shownInProgress = 6;

const underWay = (t: Task) =>
  !finished(t) && t.status !== "waiting" && t.status !== "queued";

function landedToday(t: Task) {
  const at = recordedTime(t.updated_at);
  return (
    (t.status === "landed" || t.status === "delivered") &&
    !!at &&
    at.toDateString() === new Date().toDateString()
  );
}

/**
 * What needs the owner, first and alone at full size; everything else is one
 * line each, so the page stays short when nothing is wrong.
 */
export function InboxPage({
  state,
  refresh,
  onNew,
}: {
  state: State;
  refresh: () => Promise<void>;
  onNew: () => void;
}) {
  const decisions = pendingDecisions(state.decisions);
  const needs = decisions.length + state.pending_operations.length;
  const project = (id?: string) => state.projects.find((p) => p.id === id);
  const task = (id?: string) => state.tasks.find((t) => t.id === id);
  const inProgress = state.tasks
    .filter(underWay)
    .sort((a, b) => (b.updated_at ?? "").localeCompare(a.updated_at ?? ""));
  const landed = state.tasks.filter(landedToday);
  const past = state.decisions
    .filter((d) => d.status === "resolved" || d.status === "dismissed")
    .reverse();
  if (!state.projects.length)
    return (
      <div className="page">
        <header className="page-header">
          <h1>Inbox</h1>
        </header>
        <div className="empty card">
          <p>No projects yet.</p>
          <button className="btn btn-primary" onClick={onNew}>
            New project
          </button>
        </div>
      </div>
    );
  const working = inProgress.length;
  return (
    <div className="page inbox">
      <header className="page-header">
        <h1>Inbox</h1>
        <p className="muted">
          {[
            needs ? `${needs} need you` : "Nothing needs you",
            working ? `${working} under way` : "",
          ]
            .filter(Boolean)
            .join(" · ")}
        </p>
      </header>
      {needs > 0 && (
        <section className="section" aria-label="Needs you">
          {state.pending_operations.map((operation) => (
            <InterruptedCard
              key={operation.id}
              operation={operation}
              project={project(operation.project_id)}
              refresh={refresh}
            />
          ))}
          {decisions.map((d) => (
            <DecisionCard
              key={d.id}
              decision={d}
              project={project(d.project_id)}
              task={task(d.task_id)}
              refresh={refresh}
            />
          ))}
        </section>
      )}
      {working > 0 && (
        <section className="section" aria-labelledby="inbox-progress">
          <div className="section-title">
            <h2 id="inbox-progress">Under way</h2>
          </div>
          <ul className="card rows">
            {inProgress.slice(0, shownInProgress).map((t) => (
              <ProgressRow
                key={t.id}
                task={t}
                project={project(t.project_id)}
              />
            ))}
          </ul>
          {working > shownInProgress && (
            <p className="muted small">
              And {working - shownInProgress} more on their projects' boards.
            </p>
          )}
        </section>
      )}
      {landed.length > 0 && (
        <p className="landed-today card">
          <Pill tone="done" dot>
            {landed.length} done today
          </Pill>
          <span className="landed-names">
            {landed.map((t, i) => (
              <span key={t.id}>
                {i > 0 && " · "}
                <a href={requestHref(t.project_id, t.id)}>{t.objective}</a>
              </span>
            ))}
          </span>
        </p>
      )}
      {past.length > 0 && <PastDecisions decisions={past} state={state} />}
    </div>
  );
}

function ProgressRow({ task, project }: { task: Task; project?: Project }) {
  return (
    <li>
      <a className="progress-row" href={requestHref(task.project_id, task.id)}>
        <span
          className="dot"
          style={{ color: `var(--${requestTone(task) || "wait"})` }}
        />
        <span className="progress-main">
          <span className="progress-title">{task.objective}</span>
          <span className="muted small">{project?.title}</span>
        </span>
        <span className="progress-step">{requestStep(task)}</span>
        <span className="muted small progress-since">
          {sinceLabel(task.updated_at)}
        </span>
      </a>
    </li>
  );
}

function InterruptedCard({
  operation,
  project,
  refresh,
}: {
  operation: PendingOperation;
  project?: Project;
  refresh: () => Promise<void>;
}) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!note.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      await api(
        `/api/operations/${encodeURIComponent(operation.id)}/acknowledge`,
        { method: "POST", body: JSON.stringify({ note: note.trim() }) },
      );
      await refresh();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="decision card" onSubmit={submit}>
      <div className="decision-meta">
        <Pill tone="block" dot>
          Check what happened
        </Pill>
        {project && <a href={projectHref(project.id)}>{project.title}</a>}
      </div>
      <h3 className="decision-title">{operation.summary}</h3>
      <p className="decision-context">
        crew-assistant stopped before it could confirm this finished. Check it
        yourself, then note what you found. Noting it doesn't retry anything.
      </p>
      <label className="control">
        What did you find?
        <textarea
          className="field"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          required
          maxLength={10000}
          rows={2}
        />
      </label>
      <ErrorNotice error={error} />
      <div className="actions">
        <button className="btn btn-primary" disabled={busy || !note.trim()}>
          Save note
        </button>
      </div>
    </form>
  );
}

function PastDecisions({
  decisions,
  state,
}: {
  decisions: Decision[];
  state: State;
}) {
  return (
    <details className="disclosure past-decisions">
      <summary>Past decisions ({decisions.length})</summary>
      <ul className="card rows">
        {decisions.map((d) => {
          const project = state.projects.find((p) => p.id === d.project_id);
          const dismissed = d.status === "dismissed";
          return (
            <li key={d.id} className="past-row">
              <span className="past-title">{d.title}</span>
              <span className="muted small">
                {dismissed
                  ? `Closed without deciding${d.resolution_reason ? `: ${d.resolution_reason}` : ""}`
                  : `You chose: ${d.answer}`}
              </span>
              <span className="muted small">
                {project && (
                  <a href={projectHref(project.id)}>{project.title}</a>
                )}
                {project && d.resolved_at && " · "}
                {d.resolved_at && (
                  <time dateTime={d.resolved_at}>
                    {dateLabel(d.resolved_at)}
                  </time>
                )}
              </span>
            </li>
          );
        })}
      </ul>
    </details>
  );
}
