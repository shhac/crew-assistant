import { useEffect, useRef, useState, type ReactNode } from "react";
import { Avatar } from "./Avatar";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { engineLabel } from "./engines";
import {
  kindLabel,
  kindsLabel,
  memberOf,
  roleAtWork,
  taskRoles,
} from "./members";
import { MessageForm, MessageView } from "./TeamThread";
import { isCode, taskPlaybook } from "./stages";
import { waitingLine, workingParts } from "./turns";
import { ErrorNotice, Icon, sinceLabel } from "./ui";
import {
  errorText,
  turnSteps,
  type Project,
  type State,
  type Task,
  type TeamMessage,
  type TurnStep,
} from "./api";

/** Past this, a reply shows its opening until the owner asks for the rest. */
const replyFold = 700;

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
  const end = useRef<HTMLDivElement>(null);
  const role = taskRoles(task, project).find((r) => r.name === seat);
  const member = memberOf(role, state.members);
  const { steps, error } = useSteps(task, seat, state);
  useEffect(() => back.current?.focus(), []);
  // Opened at the newest, beside the box, once there is something to show.
  const shown = useRef(false);
  useEffect(() => {
    if (shown.current || !steps?.length) return;
    shown.current = true;
    end.current?.scrollIntoView?.({ block: "end" });
  }, [steps]);
  const messages = (task.messages ?? []).filter((m) => m.to === seat);
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
        {member && <Avatar of={member} size={40} />}
        <div>
          <h2 id="member-title">{seat}</h2>
          {role && (
            <p className="muted small">
              {[
                kindsLabel(role.kinds),
                [engineLabel(role.engine), role.model]
                  .filter(Boolean)
                  .join(" "),
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>
          )}
        </div>
      </header>
      {!role ? (
        <p className="muted">{seat} isn't on this request's team.</p>
      ) : (
        <>
          <MemberStatus task={task} seat={seat} state={state} steps={steps} />
          <ErrorNotice error={error} />
          <Timeline
            steps={steps ?? []}
            messages={messages}
            seat={seat}
            loaded={!!steps}
            renderMessage={(m) => (
              <MessageView key={m.id} message={m} member={member} made={made} />
            )}
          />
          <div ref={end} />
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

type Entry =
  | { at: string; step: TurnStep; message?: undefined }
  | { at: string; message: TeamMessage; step?: undefined };

/**
 * Steps and messages as one conversation, oldest first so the newest sits
 * by the box. A message with no time goes last.
 */
function Timeline({
  steps,
  messages,
  seat,
  loaded,
  renderMessage,
}: {
  steps: TurnStep[];
  messages: TeamMessage[];
  seat: string;
  loaded: boolean;
  renderMessage: (m: TeamMessage) => ReactNode;
}) {
  const entries: Entry[] = [
    ...steps.map((step) => ({ at: step.at, step })),
    ...messages.map((message) => ({ at: message.at ?? "", message })),
  ];
  const time = (at: string) => Date.parse(at) || Number.MAX_SAFE_INTEGER;
  entries.sort((a, b) => time(a.at) - time(b.at));
  if (!entries.length)
    return (
      <p className="muted small">
        {loaded
          ? `Nothing from ${seat} on this request yet.`
          : "Loading what they've done…"}
      </p>
    );
  return (
    <ol className="member-timeline" aria-label="Activity">
      {entries.map((e) =>
        e.message ? (
          renderMessage(e.message)
        ) : (
          <StepView key={`step-${e.step.id}`} step={e.step} seat={seat} />
        ),
      )}
    </ol>
  );
}

function StepView({ step, seat }: { step: TurnStep; seat: string }) {
  const when = sinceLabel(step.at);
  if (step.kind === "prompt")
    return (
      <li className="step step-prompt">
        <details>
          <summary>
            <span className="step-label">Prompt</span>
            <span className="muted small">
              {kindLabel(step.role)} · {when}
            </span>
          </summary>
          <pre className="step-pre">{step.text}</pre>
          {step.clipped && <Clipped />}
        </details>
      </li>
    );
  if (step.kind === "reply")
    return (
      <li className="step step-reply">
        <p className="thread-line">
          <span className="thread-who">{seat}</span>
          <span className="muted small">{when}</span>
        </p>
        <Folded text={step.text ?? ""} />
        {step.clipped && <Clipped />}
      </li>
    );
  const status = step.status ?? "completed";
  const mark = status === "completed" ? "✓" : status === "running" ? "•" : "!";
  return (
    <li className={`step step-tool tool-${status}`}>
      <details>
        <summary>
          <span className="tool-mark" aria-hidden="true">
            {mark}
          </span>
          <code>{step.tool || "A tool"}</code>
          <span className="step-gist">{gist(step.input)}</span>
          <span className="muted small">{toolOutcome(step)}</span>
        </summary>
        <div className="step-detail">
          {step.input && (
            <>
              <p className="label">Input</p>
              <pre className="step-pre">{pretty(step.input)}</pre>
            </>
          )}
          {step.output ? (
            <>
              <p className="label">Output</p>
              <pre className="step-pre">{step.output}</pre>
            </>
          ) : (
            status !== "running" && <p className="muted small">No output.</p>
          )}
          {step.clipped && <Clipped />}
        </div>
      </details>
    </li>
  );
}

const Clipped = () => (
  <p className="muted small">Cut to its first part; the rest wasn't kept.</p>
);

/** "Failed · exit 1", "Running", "Stopped before it finished". */
function toolOutcome(step: TurnStep) {
  const words: Record<string, string> = {
    running: "Running",
    completed: "Done",
    failed: "Failed",
    interrupted: "Stopped before it finished",
  };
  const status = step.status ?? "completed";
  const said = words[status] ?? status;
  return step.exit_code !== undefined && step.exit_code !== 0
    ? `${said} · exit ${step.exit_code}`
    : said;
}

/** The part of a tool's input that says what it did: its command or path. */
function gist(input?: string) {
  if (!input) return "";
  let value: unknown = input;
  try {
    value = JSON.parse(input);
  } catch {
    // Cut short, so no longer JSON; its opening still says something.
  }
  let line = "";
  if (typeof value === "string") line = value;
  else if (value && typeof value === "object") {
    const fields = value as Record<string, unknown>;
    const key = [
      "command",
      "cmd",
      "file_path",
      "path",
      "pattern",
      "query",
      "url",
      "description",
    ].find((k) => typeof fields[k] === "string");
    const first = Object.values(fields).find((v) => typeof v === "string");
    line = String(key ? fields[key] : (first ?? ""));
  }
  line = line.replace(/\s+/g, " ").trim();
  return line.length > 90 ? `${line.slice(0, 90)}…` : line;
}

function pretty(input: string) {
  try {
    return JSON.stringify(JSON.parse(input), null, 2);
  } catch {
    return input;
  }
}

/** A reply in full when it is short, and its opening until asked otherwise. */
function Folded({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > replyFold;
  return (
    <div className="step-text">
      <ConversationMarkdown
        content={
          long && !open ? `${text.slice(0, replyFold).trimEnd()}…` : text
        }
      />
      {long && (
        <button
          type="button"
          className="link-button small"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}
