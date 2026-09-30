import { useState, type FormEvent } from "react";
import {
  finished,
  isCode,
  isOpenMessage,
  taskPlaybook,
  verdictOutcome,
} from "./stages";
import { kindLabel, memberOf, workingKind } from "./members";
import { stageFlow } from "./taskFlow";
import { MemberIdentity } from "./MemberIdentity";
import { Avatar } from "./Avatar";
import { ErrorNotice, Icon, Pill, counted, sinceLabel, useAction } from "./ui";
import {
  messageTeam,
  type Member,
  type Project,
  type Role,
  type State,
  type Task,
  type TeamMessage,
} from "./api";

/** What a message to each kind of team member does. */
function effect(role: Role | undefined, task: Task, waitingOn?: string) {
  if (!role) return "";
  if (workingKind(role) === "implementer") {
    if (task.status === "landing")
      return "It's landing now. Message again once it has landed.";
    if (waitingOn === "failure") return "Goes into the round after you retry.";
    if (!task.revisions?.length) return "Goes into the first round.";
    if (task.status === "waiting" || task.status === "awaiting")
      return "Sends it back for another round with your note.";
    return "Goes into the next round.";
  }
  if (!task.revisions?.length)
    return "There's nothing to check until the first version is done.";
  return `${role.name} checks the latest draft now, with your note.`;
}

/**
 * The box's prompt, by the kind of role rather than its name: a role can be
 * a member with a name of its own, which must not be lowercased.
 */
function prompt(role: Role | undefined, code: boolean) {
  const kind = role ? workingKind(role) : "";
  if (kind === "implementer")
    return `Tell the ${code ? "implementer" : "writer"} what to change`;
  if (kind === "qa") return "Ask QA to check something";
  return "Ask the reviewer to check something";
}

/** Ordered task stages; member panels remain addressed by seat. */
export function TeamFlow({
  project,
  task,
  state,
  onOpen,
}: {
  project: Project;
  task: Task;
  state: State;
  onOpen: (seat: string, stage: string) => void;
}) {
  const rows = stageFlow(task, project, state);
  if (!rows.length) return null;
  // Counts belong to the current, then next, then last occurrence of a seat.
  const countRows = new Map<string, string>();
  for (const row of rows) {
    if (!row.role) continue;
    const own = rows.filter((r) => r.role?.name === row.role?.name);
    countRows.set(
      row.role.name,
      (own.find((r) => r.state === "working" || r.state === "interrupted") ??
        own.find((r) => r.state === "not-started") ??
        own.at(-1))!.key,
    );
  }
  return (
    <section className="section" aria-label="Team">
      <h3>Team</h3>
      <ol className="task-stage-flow">
        {rows.map((row) => {
          const messages =
            countRows.get(row.role?.name ?? "") === row.key
              ? (task.messages ?? []).filter((m) => m.to === row.role?.name)
              : [];
          const open = messages.filter(isOpenMessage).length;
          const content = (
            <>
              <span
                className="task-stage-marker"
                aria-hidden="true"
                data-progress={row.state}
              >
                {row.state === "working" ? (
                  <span className="dot" />
                ) : (
                  <Icon
                    name={row.state === "done" ? "Check" : "Clock"}
                    size={18}
                  />
                )}
              </span>
              <span className="task-stage-content">
                <strong>
                  {row.label}
                  {row.roundLabel && ` · ${row.roundLabel}`}
                </strong>
                {row.role && (
                  <MemberIdentity
                    role={row.role}
                    member={memberOf(row.role, state.members)}
                    detail={kindLabel(row.kind)}
                  />
                )}
                <span className="soft small">{row.support}</span>
                {row.exception?.tone === "block" && (
                  <span className="soft small">{row.exception.text}</span>
                )}
              </span>
              <span className="task-stage-meta small">
                <Pill
                  tone={
                    row.state === "working"
                      ? "work"
                      : row.state === "done"
                        ? "done"
                        : row.state === "interrupted"
                          ? ""
                          : "wait"
                  }
                  dot={row.state === "working"}
                >
                  {row.state === "working"
                    ? "Working now"
                    : row.state === "done"
                      ? "Done"
                      : row.state === "interrupted"
                        ? "Incomplete"
                        : "Not started"}
                </Pill>
                {row.exception && (
                  <Pill
                    tone={
                      row.exception.tone === "stopped" ? "" : row.exception.tone
                    }
                  >
                    {row.exception.tone === "block" && (
                      <Icon name="Alert" size={14} />
                    )}{" "}
                    {row.exception.tone === "block"
                      ? "Blocked"
                      : row.exception.text}
                  </Pill>
                )}
                {open > 0 ? (
                  <span className="muted">
                    {counted(open, "message")} waiting
                  </span>
                ) : messages.length > 0 ? (
                  <span className="muted">
                    {counted(messages.length, "message")}
                  </span>
                ) : null}
                {row.role && <Icon name="Chevron" size={14} />}
              </span>
            </>
          );
          return (
            <li key={row.key} data-progress={row.state}>
              {row.role ? (
                <button
                  type="button"
                  className="task-stage-row"
                  data-seat={row.role.name}
                  data-stage={row.key}
                  onClick={() => onOpen(row.role!.name, row.key)}
                >
                  {content}
                </button>
              ) : (
                <div className="task-stage-row">{content}</div>
              )}
              {row.previous.length > 0 && (
                <details className="task-stage-previous small">
                  <summary>Previous work ({row.previous.length})</summary>
                  <ul>
                    {row.previous.map((p, i) => (
                      <li key={i}>
                        {p.label} · {p.outcome}
                        {p.at && ` · ${sinceLabel(p.at)}`}
                      </li>
                    ))}
                  </ul>
                </details>
              )}
            </li>
          );
        })}
      </ol>
    </section>
  );
}

/** Where a message to a seat waits until it is sent, surviving a reload. */
const draftKey = (task: Task, seat: string) =>
  `crew-assistant.message.${task.id}.${seat}`;

function useDraft(key: string) {
  const [text, setText] = useState(() => {
    try {
      return sessionStorage.getItem(key) ?? "";
    } catch {
      return "";
    }
  });
  const keep = (next: string) => {
    setText(next);
    try {
      if (next) sessionStorage.setItem(key, next);
      else sessionStorage.removeItem(key);
    } catch {
      // A draft that can't be kept still lasts while the page is open.
    }
  };
  return [text, keep] as const;
}

/**
 * Talking directly to one member of the team: the implementer takes it as
 * direction; a reviewer or QA checks the latest draft with it in mind. A seat
 * that only researches, designs or keeps the list is done before the work
 * starts, so nothing reaches it.
 */
export function MessageForm({
  project,
  task,
  role,
  waitingOn,
  refresh,
}: {
  project: Project;
  task: Task;
  role: Role;
  /** The kind of decision the request waits on, if any. */
  waitingOn?: string;
  refresh: () => Promise<void>;
}) {
  const [text, setText] = useDraft(draftKey(task, role.name));
  const { busy, error, run } = useAction();
  const code = isCode(taskPlaybook(task, project));
  if (finished(task))
    return (
      <p className="muted small">
        This request is finished. Ask for a new one to change it.
      </p>
    );
  if (!workingKind(role))
    return (
      <p className="muted small">
        Only the seats that write and check the work take messages.
      </p>
    );
  async function send(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await messageTeam(task.project_id, task.id, role.name, text.trim());
      setText("");
      await refresh();
    });
  }
  return (
    <form className="thread-form" onSubmit={send}>
      <label className="sr-only" htmlFor="thread-text">
        Message
      </label>
      <textarea
        id="thread-text"
        className="field"
        rows={3}
        value={text}
        maxLength={8192}
        placeholder={prompt(role, code)}
        onChange={(e) => setText(e.target.value)}
      />
      <p className="hint">{effect(role, task, waitingOn)}</p>
      <ErrorNotice error={error} />
      <div className="actions">
        <button className="btn btn-primary" disabled={busy || !text.trim()}>
          Send to {role.name}
        </button>
      </div>
    </form>
  );
}

export function MessageView({
  message: m,
  member,
  made,
}: {
  message: TeamMessage;
  /** The member the message went to, if it went to one. */
  member?: Member;
  /** What a round makes: a draft, or a change for code. */
  made: string;
}) {
  return (
    <li className="thread-message">
      <p className="thread-line">
        <span className="thread-who">
          {m.from === "assistant" ? "The assistant" : "You"} →{" "}
          {member && <Avatar of={member} size={16} />}
          {m.to}
        </span>
        {m.at && <span className="muted small">{sinceLabel(m.at)}</span>}
      </p>
      <p className="thread-text">{m.text}</p>
      <Reply message={m} made={made} />
    </li>
  );
}

function Reply({ message: m, made }: { message: TeamMessage; made: string }) {
  switch (m.status) {
    case "waiting":
      return <p className="thread-reply muted small">Not picked up yet</p>;
    case "working":
      return (
        <p className="thread-reply small">
          <Pill tone="work" dot>
            {m.to} is on it
          </Pill>
        </p>
      );
    case "failed":
      return (
        <p className="thread-reply small">
          <Pill tone="block">Couldn't answer</Pill> {m.reply}
        </p>
      );
    case "closed":
      return <p className="thread-reply muted small">{m.reply}</p>;
  }
  return (
    <div className="thread-reply">
      <p className="small">
        <strong>{m.to}</strong>
        {m.outcome && (
          <>
            {" "}
            <Pill tone={verdictOutcome[m.outcome]?.tone ?? "needs"}>
              {verdictOutcome[m.outcome]?.label ?? m.outcome}
            </Pill>
          </>
        )}
        {m.revision ? (
          <span className="muted">
            {" "}
            · {m.kind === "implementer" ? "in" : "on"} {made} {m.revision}
          </span>
        ) : null}
      </p>
      {m.reply && <p className="thread-text">{m.reply}</p>}
    </div>
  );
}
