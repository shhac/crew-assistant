import { useState, type FormEvent } from "react";
import { ErrorNotice, sinceLabel, useAction } from "./ui";
import {
  addNote,
  undoTaskEdit,
  type Note,
  type Task,
  type TaskEdit,
} from "./api";

/** Who wrote a note or made an edit, as the owner reads it. */
function who(by: string, kind: string) {
  if (kind === "owner") return "You";
  if (kind === "assistant") return "The assistant";
  return by;
}

/**
 * The notes the team, the assistant and the owner left on a request for
 * each other, beside its record, with a box to add one.
 */
export function RequestNotes({
  task,
  closed,
  refresh,
}: {
  task: Task;
  closed: boolean;
  refresh: () => Promise<void>;
}) {
  const [text, setText] = useState("");
  const { busy, error, run } = useAction();
  const notes: Note[] = task.notes ?? [];
  async function send(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await addNote(task.project_id, task.id, text.trim());
      setText("");
      await refresh();
    });
  }
  return (
    <section className="section thread" aria-label="Notes">
      <h3>Notes</h3>
      {notes.length > 0 && (
        <ol className="thread-messages">
          {notes.map((n) => (
            <li key={n.id} className="thread-message">
              <p className="thread-line">
                <span className="thread-who">{who(n.by, n.kind)}</span>
                {n.at && (
                  <span className="muted small">{sinceLabel(n.at)}</span>
                )}
              </p>
              <p className="thread-text">{n.text}</p>
            </li>
          ))}
        </ol>
      )}
      <form className="thread-form" onSubmit={send}>
        <label className="sr-only" htmlFor="note-text">
          Note
        </label>
        <textarea
          id="note-text"
          className="field"
          rows={2}
          value={text}
          maxLength={2000}
          placeholder="Leave a note for the team"
          onChange={(e) => setText(e.target.value)}
        />
        <p className="hint">
          {closed
            ? "Your note stays with the finished request for anyone who looks later."
            : "Everyone who works on it reads the notes. To change what it asks for, message the team instead."}
        </p>
        <ErrorNotice error={error} />
        <div className="actions">
          <button className="btn" disabled={busy || !text.trim()}>
            Add note
          </button>
        </div>
      </form>
    </section>
  );
}

/**
 * Every change to the request's title and requirements, newest first, with
 * what it replaced, each of which the owner can undo.
 */
export function RequestEdits({
  task,
  closed,
  refresh,
}: {
  task: Task;
  closed: boolean;
  refresh: () => Promise<void>;
}) {
  const { busy, error, run } = useAction();
  const edits: TaskEdit[] = [...(task.edits ?? [])].reverse();
  if (!edits.length) return null;
  return (
    <section className="section" aria-label="Changes to what was asked">
      <h3>Changes to what was asked</h3>
      {edits.map((e) => (
        <div key={e.id} className="plan-part">
          <p className="label">
            {who(e.by, e.kind)} {e.undoes ? "undid a change" : "changed it"}
            {e.at && ` · ${sinceLabel(e.at)}`}
          </p>
          <EditDiff edit={e} />
          {!closed && (
            <button
              type="button"
              className="link-button small"
              disabled={busy}
              aria-label={`Undo ${who(e.by, e.kind)}'s change`}
              onClick={() =>
                void run(async () => {
                  await undoTaskEdit(task.project_id, task.id, e.id);
                  await refresh();
                })
              }
            >
              Undo
            </button>
          )}
        </div>
      ))}
      <ErrorNotice error={error} />
    </section>
  );
}

function EditDiff({ edit }: { edit: TaskEdit }) {
  const before = edit.before.criteria ?? [];
  const after = edit.after.criteria ?? [];
  const added = after.filter((c) => !before.includes(c));
  const removed = before.filter((c) => !after.includes(c));
  return (
    <ul className="plan-list">
      {edit.before.objective !== edit.after.objective && (
        <li>
          Title: <s>{edit.before.objective}</s> → {edit.after.objective}
        </li>
      )}
      {added.map((c, i) => (
        <li key={`a${i}`}>Added: {c}</li>
      ))}
      {removed.map((c, i) => (
        <li key={`r${i}`}>
          Removed: <s>{c}</s>
        </li>
      ))}
    </ul>
  );
}
