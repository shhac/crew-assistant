import { useEffect, useRef, useState } from "react";
import { MemberIdentity } from "./MemberIdentity";
import { stageFlow } from "./taskFlow";
import { holds, memberOf, roleAtWork, taskRoles } from "./members";
import { MessageForm, MessageView } from "./TeamThread";
import { isCode, taskPlaybook } from "./stages";
import { waitingLine, workingParts } from "./turns";
import { ErrorNotice, Icon, sinceLabel } from "./ui";
import { Timeline } from "./MemberTimeline";
import {
  errorText,
  turnSteps,
  type Project,
  type State,
  type Task,
  type TurnStep,
} from "./api";

/**
 * One team member on one request: what it has been asked, what it wrote and
 * what it ran, as its sessions reported them, with the messages sent to it
 * and the box to send another. It shows; it offers nothing else to do.
 */
export function MemberPanel({
  project,
  task,
  seat,
  state,
  waitingOn,
  refresh,
  onClose,
}: {
  project: Project;
  task: Task;
  seat: string;
  state: State;
  /** The kind of decision the request waits on, if any. */
  waitingOn?: string;
  refresh: () => Promise<void>;
  onClose: () => void;
}) {
  const back = useRef<HTMLButtonElement>(null);
  const role = taskRoles(task, project).find((r) => r.name === seat);
  const member = memberOf(role, state.members);
  const { steps, error } = useSteps(task, seat, state);
  useEffect(() => back.current?.focus(), []);
  const messages = (task.messages ?? []).filter((m) => m.to === seat);
  const stages = stageFlow(task, project, state).filter(
    (r) => r.role?.name === seat,
  );
  const recordPending =
    stages.length > 0 &&
    !holds(role ?? {}, "pm") &&
    !(holds(role ?? {}, "implementer") && task.revisions?.length);
  const pending =
    stages.every((r) => r.state === "not-started" && !r.previous.length) &&
    !state.turns.some((t) => t.task_id === task.id && t.seat === seat) &&
    !messages.length &&
    task.plan?.role !== seat &&
    !task.verdicts?.some((v) => v.role === seat) &&
    !task.design?.some((d) => d.designer === seat && d.answered_at) &&
    !steps?.length &&
    (recordPending || (steps !== undefined && !error));
  const made = isCode(taskPlaybook(task, project)) ? "change" : "draft";
  return (
    <section className="member-panel" aria-labelledby="member-title">
      <div className="member-panel-bar">
        <button
          ref={back}
          type="button"
          className="btn btn-quiet btn-sm"
          onClick={onClose}
        >
          <Icon name="Up" size={14} /> Back to the request
        </button>
      </div>
      <header className="member-header">
        <MemberIdentity
          role={role ?? { name: seat, engine: "", kinds: [] }}
          member={member}
          size={40}
          headingId="member-title"
        />
      </header>
      <ErrorNotice error={error} />
      {!role ? (
        <p className="muted">{seat} isn't on this request's team.</p>
      ) : (
        <>
          {pending ? (
            <section className="task-member-pending" aria-label="Not started">
              <p>
                <Icon name="Clock" size={18} /> <strong>Not started</strong>
              </p>
              {!stages.length && (
                <p className="soft">{seat} has no stage on this request.</p>
              )}
              {[...new Set(stages.map((r) => r.support))].map((support) => (
                <p key={support} className="soft">
                  {support}
                </p>
              ))}
              <p className="muted small">
                No work from {seat} on this request yet.
              </p>
            </section>
          ) : (
            <>
              <MemberStatus
                task={task}
                seat={seat}
                state={state}
                steps={steps}
              />
              <Timeline
                steps={steps ?? []}
                messages={messages}
                seat={seat}
                loaded={!!steps}
                renderMessage={(m) => (
                  <MessageView
                    key={m.id}
                    message={m}
                    member={member}
                    made={made}
                  />
                )}
              />
            </>
          )}
          <MessageForm
            project={project}
            task={task}
            role={role}
            waitingOn={waitingOn}
            refresh={refresh}
          />
        </>
      )}
    </section>
  );
}

/**
 * A seat's steps on a request, read again each time the state is polled, so
 * they follow a member at work. What was shown stays while the next reading
 * is on its way, and through one that fails.
 */
function useSteps(task: Task, seat: string, state: State) {
  const [steps, setSteps] = useState<TurnStep[]>();
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    turnSteps(task.project_id, task.id, seat, abort.signal).then(
      (next) => {
        setSteps(next);
        setError("");
      },
      (e) => {
        if (!abort.signal.aborted) setError(errorText(e));
      },
    );
    return () => abort.abort();
  }, [task.project_id, task.id, seat, state]);
  return { steps, error };
}

/** At work now and how it is going, waiting to pick the request up, or neither. */
function MemberStatus({
  task,
  seat,
  state,
  steps,
}: {
  task: Task;
  seat: string;
  state: State;
  steps?: TurnStep[];
}) {
  const now = Date.now();
  const turn = state.turns.find(
    (t) => t.task_id === task.id && t.seat === seat,
  );
  if (turn)
    return (
      <p className="task-activity working" role="status">
        <span className="dot" aria-hidden="true" />
        <span>
          {workingParts(turn, now, false).join(" · ")}
          {turn.tool && (
            <>
              {" · now: "}
              <code>{turn.tool}</code>
            </>
          )}
        </span>
      </p>
    );
  const waiting =
    roleAtWork(task)?.name === seat ? waitingLine(task, state, now) : "";
  const last = steps?.at(-1)?.at;
  return (
    <p className="task-activity waiting" role="status">
      {waiting ||
        (last
          ? `Not at work on this request now · last active ${sinceLabel(last)}`
          : "Not at work on this request now")}
    </p>
  );
}
