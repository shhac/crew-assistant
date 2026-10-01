import { useEffect, useLayoutEffect, useRef } from "react";
import { DecisionCard } from "./DecisionCard";
import { Drafts } from "./Drafts";
import { pmLandingLine, wayOf } from "./landing";
import { MemberPanel } from "./MemberPanel";
import { RequestAttachments } from "./RequestAttachments";
import { RequestEdits, RequestNotes } from "./RequestNotes";
import { RequestDesign, RequestPlan, RequestResearch } from "./RequestPlan";
import { RequestRelations } from "./RequestRelations";
import { TaskActivity } from "./TaskActivity";
import { TeamFlow } from "./TeamThread";
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
  TaskRef,
  focusedElement,
  typingIn,
  useAction,
} from "./ui";
import {
  criteriaLines,
  landTask,
  stopTask,
  adoptProjectTeam,
  type Project,
  type State,
  type Task,
} from "./api";

/**
 * One request in full, beside its project's board. A team member's panel
 * opens over it; the request stays as it was underneath, so closing the
 * panel comes back to it unchanged.
 */
export function RequestPanel({
  project,
  task,
  state,
  seat,
  onSeat,
  refresh,
  onClose,
}: {
  project: Project;
  task?: Task;
  state: State;
  /** The seat whose panel is open, if one is. */
  seat?: string;
  /** Opens a seat's panel, or with none closes it. */
  onSeat: (seat?: string) => void;
  refresh: () => Promise<void>;
  onClose: () => void;
}) {
  const panel = useRef<HTMLElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const close = useRef<HTMLButtonElement>(null);
  // The page hands a fresh onClose on every refresh; focus moves only when
  // the request opens or closes, never on a refresh. Escape closes a
  // member's panel before the request.
  const closing = useRef(onClose);
  useEffect(() => {
    closing.current = seat ? () => onSeat() : onClose;
  });
  // Where the request was scrolled to and which member was open, to come
  // back to both.
  const scrolled = useRef(0);
  const lastSeat = useRef(seat);
  const lastStage = useRef<string | undefined>(undefined);
  useLayoutEffect(() => {
    const was = lastSeat.current;
    lastSeat.current = seat;
    if (seat || !was || !body.current) return;
    body.current.scrollTop = scrolled.current;
    const buttons = [
      ...body.current.querySelectorAll<HTMLButtonElement>("button[data-seat]"),
    ].filter((b) => b.dataset.seat === was);
    (
      buttons.find((b) => b.dataset.stage === lastStage.current) ?? buttons[0]
    )?.focus();
  }, [seat]);
  const openSeat = (name: string, stage: string) => {
    lastStage.current = stage;
    scrolled.current = body.current?.scrollTop ?? 0;
    onSeat(name);
  };
  useEffect(() => {
    const prior = focusedElement();
    const inside = panel.current;
    // A member's panel open from the start takes focus itself.
    if (!seat) close.current?.focus();
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
        <div ref={body} className="request-body" hidden={!!seat}>
          <header className="request-header">
            <TaskRef task={task} />
            <h2 id="request-title">{task.objective}</h2>
            <p className="request-status">
              <Pill tone={requestTone(task)} dot>
                {requestStep(task, decision, state.turns, project)}
              </Pill>
              {task.detail &&
                task.detail !==
                  requestStep(task, decision, state.turns, project) && (
                  <span className="muted small">{task.detail}</span>
                )}
            </p>
            <TaskActivity task={task} state={state} full />
            {pmLandingLine(task) && (
              <p className="muted small">{pmLandingLine(task)}</p>
            )}
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
          <TeamFlow
            project={project}
            task={task}
            state={state}
            onOpen={openSeat}
          />
          <RequestNotes task={task} closed={finished(task)} refresh={refresh} />
          {task.plan && <RequestPlan plan={task.plan} />}
          {!!task.research?.length && (
            <RequestResearch research={task.research} />
          )}
          {!!task.design?.length && (
            <RequestDesign
              task={task}
              designer={task.with_designer ? task.checking : undefined}
            />
          )}
          <RequestAttachments task={task} />
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
          <RequestEdits task={task} closed={finished(task)} refresh={refresh} />
          <RequestRelations
            project={project}
            task={task}
            tasks={state.tasks}
            members={state.members}
            refresh={refresh}
          />
        </div>
      )}
      {task && seat && (
        <div className="request-body">
          <MemberPanel
            key={seat}
            project={project}
            task={task}
            seat={seat}
            state={state}
            waitingOn={decision?.kind}
            refresh={refresh}
            onClose={() => onSeat()}
          />
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
  const moving = useAction();
  const land = project.playbook?.land;
  // A request keeps the team it started with; one waiting on the owner can
  // take on the team as it is now.
  const teamChanged =
    task.status === "waiting" &&
    !!project.playbook &&
    !!task.playbook &&
    JSON.stringify(project.playbook) !== JSON.stringify(task.playbook);
  const landsLater =
    (task.status === "delivered" &&
      isCode(project.playbook) &&
      wayOf(land) !== "branch") ||
    // A signed-off change waiting on the PM can be landed by the owner.
    !!task.pm_deciding;
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
          {task.proposal?.number
            ? "Merge the pull request"
            : task.playbook?.land?.pull_requests
              ? "Open the pull request"
              : `Land on ${land?.target}`}
        </button>
      )}
      {teamChanged && (
        <button
          className="btn"
          disabled={moving.busy}
          onClick={() =>
            void moving.run(async () => {
              await adoptProjectTeam(task.project_id, task.id);
              await refresh();
            })
          }
        >
          Use the project's current team
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
      <ErrorNotice error={stopping.error || landing.error || moving.error} />
    </div>
  );
}
