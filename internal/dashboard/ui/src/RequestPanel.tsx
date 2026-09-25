import { useEffect, useRef } from "react";
import { DecisionCard } from "./DecisionCard";
import { Drafts } from "./Drafts";
import { RequestDesign, RequestPlan } from "./RequestPlan";
import { TeamThread } from "./TeamThread";
import {
  decisionFor,
  finished,
  isCode,
  requestStep,
  requestTone,
} from "./stages";
import {
  CriteriaList,
  ErrorNotice,
  Icon,
  Pill,
  focusedElement,
  typingIn,
  useAction,
} from "./ui";
import {
  criteriaLines,
  landTask,
  stopTask,
  type Project,
  type State,
  type Task,
} from "./api";

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
  const panel = useRef<HTMLElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  // The page hands a fresh onClose on every refresh; focus moves only when
  // the request opens or closes, never on a refresh.
  const closing = useRef(onClose);
  useEffect(() => {
    closing.current = onClose;
  });
  useEffect(() => {
    const prior = focusedElement();
    const inside = panel.current;
    close.current?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (
        event.key === "Escape" &&
        !event.defaultPrevented &&
        !typingIn(event) &&
        // Covered by the chat drawer or the widened chat; Escape is theirs.
        !inside?.closest("[inert]")
      )
        closing.current();
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      // Hand focus back only if it is still here; the owner may be typing
      // somewhere else by now.
      const now = document.activeElement;
      if (!now || now === document.body || inside?.contains(now))
        prior?.focus?.();
    };
  }, []);
  // Direction that came from the team thread is already shown there.
  const fromThread = new Set(
    (task?.messages ?? [])
      .filter((m) => m.kind === "implementer")
      .map((m) => m.direction),
  );
  const said = (task?.direction ?? []).filter((_, i) => !fromThread.has(i));
  const decision = task ? decisionFor(task, state.decisions) : undefined;
  return (
    <aside
      ref={panel}
      className="request-panel"
      aria-labelledby="request-title"
    >
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
          <TeamThread
            project={project}
            task={task}
            members={state.members}
            waitingOn={decision?.kind}
            refresh={refresh}
          />
          {task.plan && <RequestPlan plan={task.plan} />}
          {!!task.design?.length && (
            <RequestDesign
              design={task.design}
              designer={task.with_designer ? task.checking : undefined}
            />
          )}
          <Drafts
            project={project}
            task={task}
            members={state.members}
            collapsed={!!decision}
          />
          {(criteriaLines(task.criteria).length > 0 || said.length > 0) && (
            <section className="section" aria-label="What was asked">
              <h3>What was asked</h3>
              <CriteriaList criteria={task.criteria} empty="" marker />
              {said.length > 0 && (
                <>
                  <p className="label">Your answers along the way</p>
                  <ul className="said">
                    {said.map((line, i) => (
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
