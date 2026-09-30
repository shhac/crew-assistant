import { useState, type FormEvent } from "react";
import {
  ErrorNotice,
  Icon,
  sinceLabel,
  useAction,
  useNewestInView,
} from "./ui";
import { sizeLabel, useFileDrop } from "./composerAssets";
import { addPending, ATTACHMENT_ACCEPT, type PendingFile } from "./attachments";
import { AttachmentList } from "./RequestAttachments";
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
 * each other, beside its record, with a box to add one. The owner's note
 * can carry files, dropped, pasted or picked as in the chat composer.
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
  const [files, setFiles] = useState<PendingFile[]>([]);
  const [refused, setRefused] = useState<string[]>([]);
  const { busy, error, run } = useAction();
  const box = useNewestInView<HTMLOListElement>("bottom");
  const notes: Note[] = task.notes ?? [];
  const attachments = task.attachments ?? [];
  function attach(picked: File[]) {
    if (!picked.length) return;
    const { pending, refused } = addPending(files, picked);
    setFiles(pending);
    setRefused(refused);
  }
  const { dragging, dropHandlers, onPaste } = useFileDrop(attach);
  async function send(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await addNote(
        task.project_id,
        task.id,
        text.trim(),
        files.map((f) => f.file),
      );
      setText("");
      setFiles([]);
      setRefused([]);
      await refresh();
    });
  }
  return (
    <section className="section thread" aria-label="Notes">
      <h3>Notes</h3>
      {notes.length > 0 && (
        <ol className="thread-messages bounded" {...box}>
          {notes.map((n) => (
            <li key={n.id} className="thread-message">
              <p className="thread-line">
                <span className="thread-who">{who(n.by, n.kind)}</span>
                {n.at && (
                  <span className="muted small">{sinceLabel(n.at)}</span>
                )}
              </p>
              {n.text && <p className="thread-text">{n.text}</p>}
              <AttachmentList
                task={task}
                attachments={attachments.filter((a) => a.note === n.id)}
              />
            </li>
          ))}
        </ol>
      )}
      <form
        className={`thread-form${dragging ? " dragging" : ""}`}
        onSubmit={send}
        {...dropHandlers}
      >
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
          onPaste={onPaste}
        />
        {files.length > 0 && (
          <ul className="attachments" aria-label="Files to attach">
            {files.map((f) => (
              <li key={f.id}>
                <span className="attachment-name">{f.file.name}</span>
                <span className="muted small">{sizeLabel(f.file.size)}</span>
                <button
                  type="button"
                  className="btn btn-quiet btn-icon btn-sm"
                  aria-label={`Remove attachment ${f.file.name}`}
                  onClick={() =>
                    setFiles((current) => current.filter((c) => c.id !== f.id))
                  }
                >
                  <Icon name="Close" size={12} />
                </button>
              </li>
            ))}
          </ul>
        )}
        {refused.length > 0 && (
          <div className="error" role="alert">
            {refused.map((line, i) => (
              <p key={`${i}:${line}`}>{line}</p>
            ))}
          </div>
        )}
        <p className="hint">
          {closed
            ? "Your note stays with the finished request for anyone who looks later."
            : "Everyone who works on it reads the notes, and can open the files. To change what it asks for, message the team instead."}
        </p>
        <ErrorNotice error={error} />
        <div className="actions">
          <label className="btn btn-quiet">
            Attach files
            <input
              type="file"
              className="sr-only"
              multiple
              accept={ATTACHMENT_ACCEPT}
              onChange={(e) => {
                attach(Array.from(e.target.files || []));
                e.target.value = "";
              }}
            />
          </label>
          <button
            className="btn"
            disabled={busy || (!text.trim() && !files.length)}
          >
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
  const box = useNewestInView<HTMLDivElement>("top");
  const edits: TaskEdit[] = [...(task.edits ?? [])].reverse();
  if (!edits.length) return null;
  return (
    <section className="section" aria-label="Changes to what was asked">
      <h3>Changes to what was asked</h3>
      <div className="bounded" {...box}>
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
      </div>
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
