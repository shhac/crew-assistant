import { useState, type FormEvent } from "react";
import {
  finished,
  isCode,
  isOpenMessage,
  taskPlaybook,
  verdictOutcome,
} from "./stages";
import {
  kindsLabel,
  memberOf,
  roleAtWork,
  taskRoles,
  workingKind,
  workingSeats,
} from "./members";
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

/**
 * A request's team, one entry per seat, each opening that member's panel.
 * The conversation itself lives in the panel; here each says only whether
 * it is at work and whether a message to it is waiting.
 */
export function TeamSeats({
  project,
  task,
  state,
  onOpen,
}: {
  project: Project;
  task: Task;
  state: State;
  onOpen: (seat: string) => void;
}) {
  const team = taskRoles(task, project);
  if (!team.length) return null;
  const working = new Set(workingSeats(task, state.turns).map((r) => r.name));
  const next = finished(task) ? undefined : roleAtWork(task)?.name;
  return (
    <section className="section" aria-label="Team">
      <h3>Team</h3>
      <ul className="seats">
        {team.map((r) => {
          const member = memberOf(r, state.members);
          const messages = (task.messages ?? []).filter((m) => m.to === r.name);
          const open = messages.filter(isOpenMessage).length;
          return (
            <li key={r.name}>
              <button
                type="button"
                className="seat"
                data-seat={r.name}
                onClick={() => onOpen(r.name)}
              >
                {member && <Avatar of={member} size={24} />}
                <span className="seat-name">{r.name}</span>
                <span className="muted small">{kindsLabel(r.kinds)}</span>
                <span className="seat-note small">
                  {working.has(r.name) ? (
                    <Pill tone="work" dot>
                      Working now
                    </Pill>
                  ) : next === r.name ? (
                    <span className="muted">Up next</span>
                  ) : null}
                  {open > 0 ? (
                    <Pill tone="wait">{counted(open, "message")} waiting</Pill>
                  ) : messages.length > 0 ? (
                    <span className="muted">
                      {counted(messages.length, "message")}
                    </span>
                  ) : null}
                </span>
                <Icon name="Chevron" size={14} />
              </button>
            </li>
          );
        })}
      </ul>
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
