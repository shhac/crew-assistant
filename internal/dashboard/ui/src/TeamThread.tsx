import { useState, type FormEvent } from "react";
import { finished } from "./stages";
import { ErrorNotice, Pill, sinceLabel, useAction } from "./ui";
import {
  messageTeam,
  type Project,
  type Role,
  type Task,
  type TeamMessage,
} from "./api";

const outcomeLabel: Record<string, string> = {
  pass: "Passed",
  revise: "Asked for changes",
  question: "Asked a question",
};

/** What a message to each kind of team member does. */
function effect(role: Role | undefined, task: Task) {
  if (!role) return "";
  if (role.kind === "implementer")
    return task.status === "waiting" || task.status === "awaiting"
      ? "Sends the request back for another round with your note."
      : `Goes into the ${role.name.toLowerCase()}'s next round. Nothing is approved or landed until it has been taken in.`;
  if (!task.revisions?.length)
    return "There's nothing to check until the first draft is done.";
  return `${role.name} checks the latest draft now, with your note.`;
}

/**
 * Talking directly to one member of the team: the implementer takes it as
 * direction; a reviewer or QA checks the latest draft with it in mind.
 */
export function TeamThread({
  project,
  task,
  refresh,
}: {
  project: Project;
  task: Task;
  refresh: () => Promise<void>;
}) {
  const team = task.roles?.length
    ? task.roles
    : (project.playbook?.roles ?? []);
  const [to, setTo] = useState(team[0]?.name ?? "");
  const [text, setText] = useState("");
  const { busy, error, run } = useAction();
  const messages = task.messages ?? [];
  const role = team.find((r) => r.name === to);
  const closed = finished(task);
  async function send(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await messageTeam(task.project_id, task.id, to, text.trim());
      setText("");
      await refresh();
    });
  }
  if (!team.length) return null;
  return (
    <section className="section thread" aria-label="Team">
      <h3>Team</h3>
      {messages.length > 0 && (
        <ol className="thread-messages">
          {messages.map((m) => (
            <MessageView key={m.id} message={m} />
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
              {team.map((r) => (
                <option key={r.name} value={r.name}>
                  {r.name}
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
            placeholder={
              role?.kind === "implementer"
                ? `Tell the ${role.name.toLowerCase()} what to change`
                : role?.kind === "qa"
                  ? "Ask QA to check something"
                  : "Ask for a review of something"
            }
            onChange={(e) => setText(e.target.value)}
          />
          <p className="hint">{effect(role, task)}</p>
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

function MessageView({ message: m }: { message: TeamMessage }) {
  return (
    <li className="thread-message">
      <p className="thread-line">
        <span className="thread-who">
          {m.from === "assistant" ? "The assistant" : "You"} → {m.to}
        </span>
        {m.at && <span className="muted small">{sinceLabel(m.at)}</span>}
      </p>
      <p className="thread-text">{m.text}</p>
      <Reply message={m} />
    </li>
  );
}

function Reply({ message: m }: { message: TeamMessage }) {
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
            <Pill tone={m.outcome === "pass" ? "done" : "needs"}>
              {outcomeLabel[m.outcome] ?? m.outcome}
            </Pill>
          </>
        )}
        {m.revision ? (
          <span className="muted">
            {" "}
            · {m.kind === "implementer" ? "in" : "on"} draft {m.revision}
          </span>
        ) : null}
      </p>
      {m.reply && <p className="thread-text">{m.reply}</p>}
    </div>
  );
}
