import { useState, type FormEvent } from "react";
import { finished, isCode, taskPlaybook, verdictOutcome } from "./stages";
import { kindWord, roleMember, taskRoles, workingKind } from "./members";
import { Avatar } from "./Avatar";
import { ErrorNotice, Pill, sinceLabel, useAction } from "./ui";
import {
  messageTeam,
  type Member,
  type Project,
  type Role,
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
 * How a seat is offered in "To": by its name, and by the role a message
 * reaches when the seat also researches or designs, since that isn't what it
 * answers.
 */
function recipient(role: Role, code: boolean) {
  if (role.kinds.length < 2) return role.name;
  const kind = workingKind(role);
  const word = kind === "implementer" && !code ? "writer" : kindWord(kind);
  return `${role.name} (${word})`;
}

/**
 * Talking directly to one member of the team: the implementer takes it as
 * direction; a reviewer or QA checks the latest draft with it in mind.
 */
export function TeamThread({
  project,
  task,
  members,
  waitingOn,
  refresh,
}: {
  project: Project;
  task: Task;
  members: Member[];
  /** The kind of decision the request waits on, if any. */
  waitingOn?: string;
  refresh: () => Promise<void>;
}) {
  const team = taskRoles(task, project);
  // A seat that only plans is done before the work starts; nothing reaches it.
  const reachable = team.filter((r) => workingKind(r) !== "");
  const [to, setTo] = useState(reachable[0]?.name ?? "");
  const [text, setText] = useState("");
  const { busy, error, run } = useAction();
  const messages = task.messages ?? [];
  const role = reachable.find((r) => r.name === to);
  const closed = finished(task);
  const code = isCode(taskPlaybook(task, project));
  async function send(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await messageTeam(task.project_id, task.id, to, text.trim());
      setText("");
      await refresh();
    });
  }
  if (!reachable.length) return null;
  return (
    <section className="section thread" aria-label="Team">
      <h3>Message the team</h3>
      {messages.length > 0 && (
        <ol className="thread-messages">
          {messages.map((m) => (
            <MessageView
              key={m.id}
              message={m}
              member={roleMember(team, m.to, members)}
              made={code ? "change" : "draft"}
            />
          ))}
        </ol>
      )}
      {closed ? (
        <p className="muted small">
          This request is finished. Ask for a new one to change it.
        </p>
      ) : (
        <form className="thread-form" onSubmit={send}>
          <div className="thread-to">
            <label htmlFor="thread-to" className="label">
              To
            </label>
            <select
              id="thread-to"
              className="field"
              value={to}
              onChange={(e) => setTo(e.target.value)}
            >
              {reachable.map((r) => (
                <option key={r.name} value={r.name}>
                  {recipient(r, code)}
                </option>
              ))}
            </select>
          </div>
          <label className="sr-only" htmlFor="thread-text">
            Message
          </label>
          <textarea
            id="thread-text"
            className="field"
            rows={2}
            value={text}
            maxLength={8192}
            placeholder={prompt(role, code)}
            onChange={(e) => setText(e.target.value)}
          />
          <p className="hint">{effect(role, task, waitingOn)}</p>
          <ErrorNotice error={error} />
          <div className="actions">
            <button className="btn btn-primary" disabled={busy || !text.trim()}>
              Send to {to}
            </button>
          </div>
        </form>
      )}
    </section>
  );
}

function MessageView({
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
