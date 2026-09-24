import { useState, type FormEvent } from "react";
import { CriteriaList, dateLabel, ErrorNotice, Status, useAction } from "./ui";
import { taskDetail, taskStatusLine } from "./taskStatus";
import {
  askForTask,
  criteriaLines,
  landTask,
  stopTask,
  type Project,
  type Task,
  type Verdict,
} from "./api";

export function decisionAnchor(decisionID: string) {
  return `decision-${decisionID}`;
}

function askBlocker(project: Project) {
  if (!project.brief.goal)
    return "Write the brief first, so the work has something to answer to.";
  if (!project.playbook)
    return "Choose a team first, so someone can do the work.";
  return "";
}

export function AskForSomething({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [objective, setObjective] = useState("");
  const [criteria, setCriteria] = useState("");
  const { busy, error, run } = useAction();
  const blocker = askBlocker(project);
  async function ask(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await askForTask(project.id, {
        objective: objective.trim(),
        criteria: criteriaLines(criteria),
      });
      setObjective("");
      setCriteria("");
      await refresh();
    });
  }
  return (
    <form
      className="project-card project-card-form"
      aria-label="Ask for something"
      onSubmit={ask}
    >
      <h2>Ask for something</h2>
      {blocker && <p className="muted">{blocker}</p>}
      <fieldset disabled={!!blocker || busy}>
        <label htmlFor="ask-objective">
          What do you want?
          <textarea
            id="ask-objective"
            value={objective}
            onChange={(e) => setObjective(e.target.value)}
            placeholder="A one-page summary of the plan for the team"
            rows={2}
            maxLength={20000}
            required
          />
        </label>
        <label htmlFor="ask-criteria">
          How will you judge it? (optional)
          <textarea
            id="ask-criteria"
            value={criteria}
            onChange={(e) => setCriteria(e.target.value)}
            placeholder="One point per line, on top of the brief"
            rows={2}
            maxLength={20000}
          />
        </label>
        <ErrorNotice error={error} />
        <div className="form-actions">
          <button
            className="button primary"
            type="submit"
            disabled={!objective.trim()}
          >
            {busy ? "Asking…" : "Ask"}
          </button>
        </div>
      </fieldset>
    </form>
  );
}

function scrollToDecision(decisionID: string) {
  document
    .getElementById(decisionAnchor(decisionID))
    ?.scrollIntoView?.({ behavior: "smooth", block: "start" });
}

export function TaskList({
  tasks,
  hasTeam,
  landsOn,
  refresh,
}: {
  tasks: Task[];
  hasTeam: boolean;
  landsOn?: string;
  refresh: () => Promise<void>;
}) {
  if (!tasks.length)
    return (
      <p className="muted">Nothing asked for yet. What you ask appears here.</p>
    );
  return (
    <ul className="task-list" aria-label="Requests">
      {tasks.map((task) => (
        <TaskRow
          key={task.id}
          task={task}
          hasTeam={hasTeam}
          landsOn={landsOn}
          refresh={refresh}
        />
      ))}
    </ul>
  );
}

function TaskRow({
  task,
  hasTeam,
  landsOn,
  refresh,
}: {
  task: Task;
  hasTeam: boolean;
  landsOn?: string;
  refresh: () => Promise<void>;
}) {
  const stopping = useAction();
  const landing = useAction();
  const error = stopping.error || landing.error;
  const status = taskStatusLine(task, hasTeam);
  const detail = taskDetail(task);
  const decisionID = task.status === "waiting" ? task.decision_id : "";
  const finished =
    task.status === "delivered" ||
    task.status === "landed" ||
    task.status === "stopped";
  const landsElsewhere = !!landsOn && task.status === "delivered";
  const land = () =>
    landing.run(async () => {
      await landTask(task.project_id, task.id);
      await refresh();
    });
  const stop = () =>
    stopping.run(async () => {
      await stopTask(task.project_id, task.id);
      await refresh();
    });
  return (
    <li className="task-row">
      <div className="task-row-text">
        <strong>{task.objective}</strong>
        {detail && <span>{detail}</span>}
        {error && <ErrorNotice error={error} />}
      </div>
      <div className="task-row-actions">
        {landsElsewhere && (
          <button
            type="button"
            className="text-button"
            disabled={landing.busy}
            onClick={land}
          >
            {landing.busy ? "Landing…" : `Land on ${landsOn}`}
          </button>
        )}
        {!finished && (
          <button
            type="button"
            className="text-button"
            disabled={stopping.busy}
            onClick={stop}
          >
            {stopping.busy ? "Stopping…" : "Stop"}
          </button>
        )}
        {decisionID ? (
          <button
            type="button"
            className="text-button task-decision-link"
            onClick={() => scrollToDecision(decisionID)}
          >
            <Status tone={status.tone} plain>
              {status.label}
            </Status>
          </button>
        ) : (
          <Status tone={status.tone} plain>
            {status.label}
          </Status>
        )}
      </div>
    </li>
  );
}

const outcomeLabel: Record<string, string> = {
  pass: "Passed",
  revise: "Asked for changes",
  question: "Asked a question",
};

/** Rounds, drafts and reviews: one click deeper than the project page. */
export function WorkHistory({
  project,
  tasks,
}: {
  project: Project;
  tasks: Task[];
}) {
  const worked = tasks.filter(
    (t) => t.revisions?.length || t.direction?.length,
  );
  if (!worked.length) return null;
  return (
    <details className="project-drilldown">
      <summary>Rounds and reviews</summary>
      {worked.map((task) => (
        <TaskHistory
          key={task.id}
          task={task}
          briefVersion={project.brief.version}
        />
      ))}
    </details>
  );
}

function TaskHistory({
  task,
  briefVersion,
}: {
  task: Task;
  briefVersion: number;
}) {
  const verdicts = task.verdicts ?? [];
  return (
    <section
      className="task-history"
      aria-label={`Rounds for ${task.objective}`}
    >
      <h3>{task.objective}</h3>
      {!!task.roles?.length && (
        <p className="field-hint">
          Team: {task.roles.map((r) => `${r.name} (${r.engine})`).join(", ")}
          {task.max_rounds ? ` · up to ${task.max_rounds} rounds` : ""}
        </p>
      )}
      {!!criteriaLines(task.criteria).length && (
        <CriteriaList criteria={task.criteria} marker />
      )}
      <ol className="round-list">
        {(task.revisions ?? []).map((revision) => (
          <li key={revision.n}>
            <h4>
              Draft {revision.n}
              {revision.at && (
                <time dateTime={revision.at}> · {dateLabel(revision.at)}</time>
              )}
            </h4>
            {revision.brief_version !== briefVersion && (
              <p className="field-hint">
                Written against brief version {revision.brief_version}
              </p>
            )}
            {revision.summary && (
              <p className="round-summary">{revision.summary}</p>
            )}
            {verdicts
              .filter((v) => v.revision === revision.n)
              .map((verdict) => (
                <VerdictView
                  key={`${verdict.role}-${verdict.brief_version}-${verdict.at}`}
                  verdict={verdict}
                  stale={verdict.brief_version !== briefVersion}
                />
              ))}
          </li>
        ))}
      </ol>
      {!!task.direction?.length && (
        <div className="owner-direction">
          <strong>Your direction</strong>
          <ul>
            {task.direction.map((line, i) => (
              <li key={i}>{line}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function VerdictView({ verdict, stale }: { verdict: Verdict; stale: boolean }) {
  return (
    <div className={`verdict ${verdict.outcome}`}>
      <p>
        <strong>{verdict.role}</strong>:{" "}
        {outcomeLabel[verdict.outcome] ?? verdict.outcome}
        {stale && (
          <span className="field-hint">
            {" "}
            (against brief version {verdict.brief_version})
          </span>
        )}
      </p>
      {verdict.summary && <p>{verdict.summary}</p>}
      {verdict.question && <p>Question: {verdict.question}</p>}
      {!!verdict.findings?.length && (
        <ul>
          {verdict.findings.map((finding, i) => (
            <li key={i}>
              {finding.criterion && <em>{finding.criterion}: </em>}
              {finding.note}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
