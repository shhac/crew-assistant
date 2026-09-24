import { useEffect, useRef } from "react";
import { DecisionCard } from "./DecisionCard";
import { DraftFiles } from "./DraftPreview";
import { TeamThread } from "./TeamThread";
import { finished, isCode, requestStep, requestTone } from "./stages";
import {
  CriteriaList,
  ErrorNotice,
  Icon,
  Pill,
  dateLabel,
  useAction,
} from "./ui";
import {
  criteriaLines,
  landTask,
  pendingDecisions,
  stopTask,
  type Project,
  type Revision,
  type State,
  type Task,
  type Verdict,
} from "./api";

const outcome: Record<string, { label: string; tone: string }> = {
  pass: { label: "Passed", tone: "done" },
  revise: { label: "Asked for changes", tone: "needs" },
  question: { label: "Asked a question", tone: "needs" },
};

/** One request in full, beside its project's board. */
export function RequestPanel({
  project,
  task,
  state,
  refresh,
  onClose,
}: {
  project: Project;
  task?: Task;
  state: State;
  refresh: () => Promise<void>;
  onClose: () => void;
}) {
  const close = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const prior = document.activeElement as HTMLElement | null;
    close.current?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) onClose();
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      prior?.focus?.();
    };
  }, [onClose]);
  const decision = task?.decision_id
    ? pendingDecisions(state.decisions).find((d) => d.id === task.decision_id)
    : undefined;
  return (
    <aside className="request-panel" aria-labelledby="request-title">
      <div className="request-panel-bar">
        <button
          ref={close}
          type="button"
          className="btn btn-quiet btn-icon"
          aria-label="Close"
          onClick={onClose}
        >
          <Icon name="Close" />
        </button>
      </div>
      {!task ? (
        <div className="request-body">
          <h2 id="request-title">Request not found</h2>
          <p className="muted">It may have been removed.</p>
        </div>
      ) : (
        <div className="request-body">
          <header className="request-header">
            <h2 id="request-title">{task.objective}</h2>
            <p className="request-status">
              <Pill tone={requestTone(task)} dot>
                {requestStep(task, decision)}
              </Pill>
              {task.detail && task.detail !== requestStep(task, decision) && (
                <span className="muted small">{task.detail}</span>
              )}
            </p>
            <RequestActions project={project} task={task} refresh={refresh} />
          </header>
          {decision && (
            <DecisionCard
              decision={decision}
              project={project}
              task={task}
              refresh={refresh}
              full
            />
          )}
          <TeamThread project={project} task={task} refresh={refresh} />
          <Drafts project={project} task={task} />
          {(criteriaLines(task.criteria).length > 0 ||
            (task.direction?.length ?? 0) > 0) && (
            <section className="section" aria-label="What was asked">
              <h3>What was asked</h3>
              <CriteriaList criteria={task.criteria} empty="" marker />
              {!!task.direction?.length && (
                <>
                  <p className="label">Said along the way</p>
                  <ul className="said">
                    {task.direction.map((line, i) => (
                      <li key={i}>{line}</li>
                    ))}
                  </ul>
                </>
              )}
            </section>
          )}
        </div>
      )}
    </aside>
  );
}

function RequestActions({
  project,
  task,
  refresh,
}: {
  project: Project;
  task: Task;
  refresh: () => Promise<void>;
}) {
  const stopping = useAction();
  const landing = useAction();
  const land = project.playbook?.land;
  const landsLater =
    task.status === "delivered" &&
    isCode(project.playbook) &&
    (land?.via === "push" || land?.via === "pull-request");
  if (finished(task) && !landsLater) return null;
  return (
    <div className="actions">
      {landsLater && (
        <button
          className="btn btn-primary"
          disabled={landing.busy}
          onClick={() =>
            void landing.run(async () => {
              await landTask(task.project_id, task.id);
              await refresh();
            })
          }
        >
          Land on {land?.target}
        </button>
      )}
      {!finished(task) && (
        <button
          className="btn btn-quiet btn-danger"
          disabled={stopping.busy}
          onClick={() =>
            void stopping.run(async () => {
              await stopTask(task.project_id, task.id);
              await refresh();
            })
          }
        >
          Stop request
        </button>
      )}
      <ErrorNotice error={stopping.error || landing.error} />
    </div>
  );
}

/** Each draft, newest first, with what every checker said about it. */
function Drafts({ project, task }: { project: Project; task: Task }) {
  const revisions = [...(task.revisions ?? [])].reverse();
  if (!revisions.length) return null;
  const code = isCode(task.playbook ?? project.playbook);
  return (
    <section className="section" aria-label="Drafts">
      <h3>{code ? "Changes" : "Drafts"}</h3>
      {revisions.map((r, i) => (
        <details key={r.n} className="draft card" open={i === 0}>
          <summary>
            <span className="draft-name">
              {code ? "Change" : "Draft"} {r.n}
            </span>
            <span className="draft-checks">
              {(task.verdicts ?? [])
                .filter((v) => v.revision === r.n)
                .map((v, j) => (
                  <Pill key={j} tone={outcome[v.outcome]?.tone}>
                    {v.role}: {outcome[v.outcome]?.label ?? v.outcome}
                  </Pill>
                ))}
            </span>
            {r.at && (
              <time className="muted small" dateTime={r.at}>
                {dateLabel(r.at)}
              </time>
            )}
          </summary>
          <DraftDetail
            project={project}
            task={task}
            revision={r}
            code={code}
            latest={i === 0}
          />
        </details>
      ))}
    </section>
  );
}

function DraftDetail({
  project,
  task,
  revision,
  code,
  latest,
}: {
  project: Project;
  task: Task;
  revision: Revision;
  code: boolean;
  latest: boolean;
}) {
  const verdicts = (task.verdicts ?? []).filter(
    (v) => v.revision === revision.n,
  );
  return (
    <div className="draft-detail">
      {revision.summary && (
        <p>
          <strong>
            {task.roles?.find((r) => r.kind === "implementer")?.name ??
              "Implementer"}
          </strong>{" "}
          {revision.summary}
        </p>
      )}
      {revision.brief_version !== project.brief.version && (
        <p className="muted small">
          Written for brief version {revision.brief_version}.
        </p>
      )}
      {verdicts.map((v, i) => (
        <VerdictView key={i} verdict={v} asked={!!v.asked} />
      ))}
      {code
        ? !!revision.files?.length && (
            <details className="disclosure">
              <summary>
                {revision.files.length} files
                {revision.ref && (
                  <>
                    {" "}
                    at <code>{revision.ref.slice(0, 7)}</code>
                  </>
                )}
              </summary>
              <ul className="file-list">
                {revision.files.map((f) => (
                  <li key={f}>
                    <code>{f}</code>
                  </li>
                ))}
              </ul>
            </details>
          )
        : latest && (
            <DraftFiles
              projectID={project.id}
              taskID={task.id}
              n={revision.n}
            />
          )}
    </div>
  );
}

function VerdictView({ verdict, asked }: { verdict: Verdict; asked: boolean }) {
  return (
    <div className="check-note">
      <p>
        <strong>{verdict.role}</strong>
        {asked && <span className="muted small"> (asked directly)</span>}{" "}
        {verdict.summary}
      </p>
      {verdict.question && <p className="soft">Question: {verdict.question}</p>}
      {!!verdict.findings?.length && (
        <ul className="findings">
          {verdict.findings.map((f, i) => (
            <li key={i}>
              {f.criterion && <span className="muted">{f.criterion}: </span>}
              {f.note}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
