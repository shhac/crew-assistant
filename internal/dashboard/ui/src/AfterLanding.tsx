import { useEffect, useId, useRef, useState } from "react";
import {
  clearOwnerChecks,
  markOwnerCheck,
  type OwnerCheckItem,
  type OwnerCheckMark,
  type OutstandingCheck,
  type Task,
} from "./api";
import { ErrorNotice, Pill, TaskRef, dateLabel, useAction } from "./ui";
import { requestHref } from "./router";

function checkWord(count: number) {
  return count === 1 ? "check" : "checks";
}

const statusLabel = {
  outstanding: "Outstanding",
  done: "Done",
  skipped: "Skipped",
};
function Mark({ mark }: { mark: OwnerCheckMark }) {
  return (
    <span className="muted small">
      {statusLabel[mark.status]} by{" "}
      {mark.by === "owner" ? "you" : "the assistant"} · {dateLabel(mark.at)}
      {mark.note ? ` · ${mark.note}` : ""}
    </span>
  );
}
function CheckRow({
  task,
  item,
  refresh,
  announce,
}: {
  task: Task;
  item: OwnerCheckItem;
  refresh: () => Promise<void>;
  announce: (message: string) => void;
}) {
  const [skipping, setSkipping] = useState(false);
  const [note, setNote] = useState("");
  const hint = useId();
  const field = useRef<HTMLTextAreaElement>(null);
  const skipButton = useRef<HTMLButtonElement>(null);
  const { busy, error, run } = useAction();
  useEffect(() => {
    if (skipping) field.current?.focus();
  }, [skipping]);
  function cancelSkip() {
    setSkipping(false);
    skipButton.current?.focus();
  }
  function mark(status: OwnerCheckMark["status"]) {
    void run(async () => {
      await markOwnerCheck(task.project_id, task.id, item.step, status, note);
      announce(`${item.step}: ${statusLabel[status]}`);
      setSkipping(false);
      setNote("");
      await refresh();
    });
  }
  return (
    <li className="after-landing-check">
      <div>{item.step}</div>
      <div>
        <Pill tone={item.status === "done" ? "done" : ""}>
          {statusLabel[item.status]}
        </Pill>
        {item.mark && item.status !== "outstanding" && (
          <Mark mark={item.mark} />
        )}
      </div>
      <div className="actions">
        {item.status === "outstanding" ? (
          <>
            <button
              aria-label={`Done: ${item.step}`}
              className="btn btn-sm"
              disabled={busy}
              onClick={() => mark("done")}
            >
              Done
            </button>
            <button
              ref={skipButton}
              aria-label={`Skip: ${item.step}`}
              className="btn btn-sm"
              disabled={busy}
              onClick={() => setSkipping(true)}
            >
              Skip
            </button>
          </>
        ) : (
          <button
            aria-label={`Undo: ${item.step}`}
            className="btn btn-sm"
            disabled={busy}
            onClick={() => mark("outstanding")}
          >
            Undo
          </button>
        )}
      </div>
      {skipping && (
        <form
          className="form"
          onSubmit={(e) => {
            e.preventDefault();
            if (note.trim()) mark("skipped");
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              cancelSkip();
            }
          }}
        >
          <label>
            Why skip this check?
            <textarea
              className="field"
              ref={field}
              required
              maxLength={500}
              aria-describedby={hint}
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          </label>
          <p id={hint} className="hint" aria-live="polite">
            {!note.trim()
              ? "A short note is required to skip this check."
              : "This note will be recorded with the skipped check."}
          </p>
          <button className="btn" type="button" onClick={cancelSkip}>
            Cancel
          </button>
          <button className="btn" disabled={busy || !note.trim()}>
            Skip check
          </button>
        </form>
      )}
      <ErrorNotice error={error} />
      {!!item.history?.length && (
        <details>
          <summary>Previous marks</summary>
          {[
            ...item.history,
            ...(item.status === "outstanding" && item.mark ? [item.mark] : []),
          ].map((m, i) => (
            <div key={i}>
              <Mark mark={m} />
            </div>
          ))}
        </details>
      )}
    </li>
  );
}
export function RequestChecks({
  task,
  refresh,
}: {
  task: Task;
  refresh: () => Promise<void>;
}) {
  const [announcement, setAnnouncement] = useState("");
  if (
    (!["landed", "delivered"].includes(task.status) &&
      !(
        [
          "researching",
          "designing",
          "writing",
          "reviewing",
          "deciding",
          "landing",
          "waiting",
          "awaiting",
        ].includes(task.status) && task.landed_at
      )) ||
    !task.checklist?.length
  )
    return null;
  return (
    <section className="section" aria-label="After landing">
      <h2>After landing</h2>
      <p className="muted small">These checks do not block landing.</p>
      <span className="muted small" role="status">
        {announcement}
      </span>
      <ul className="card rows">
        {task.checklist.map((item) => (
          <CheckRow
            key={item.step}
            task={task}
            item={item}
            refresh={refresh}
            announce={setAnnouncement}
          />
        ))}
      </ul>
    </section>
  );
}
export function InboxAfterLanding({
  checks,
  empty = false,
}: {
  checks: OutstandingCheck[];
  empty?: boolean;
}) {
  if (!checks.length)
    return empty ? (
      <p className="muted">No after-landing checks outstanding.</p>
    ) : null;
  return (
    <section
      className="section"
      id="after-landing"
      tabIndex={-1}
      aria-label="After landing"
    >
      <div className="section-title">
        <h2>After landing</h2>
        <span className="muted small">
          {checks.length} {checkWord(checks.length)} outstanding
        </span>
      </div>
      <ul className="card rows">
        {checks.map((c) => (
          <li key={`${c.task_id}:${c.step}`}>
            <a
              className="after-landing-link"
              href={requestHref(c.project_id, c.task_id)}
              aria-label={`${c.step} · ${c.ref} · ${c.objective} · ${c.project} · Landed ${dateLabel(c.landed_at)}`}
            >
              <span>{c.step}</span>
              <span className="muted small">
                <TaskRef task={{ ref: c.ref }} /> · {c.project} · Landed{" "}
                {dateLabel(c.landed_at)}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  );
}
export function ProjectChecks({
  project,
  checks,
  refresh,
}: {
  project: string;
  checks: OutstandingCheck[];
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [result, setResult] = useState("");
  return (
    <>
      {checks.length > 0 && (
        <Pill>
          {checks.length} {checkWord(checks.length)} outstanding
        </Pill>
      )}
      <span role="status" className={result ? "muted small" : "sr-only"}>
        {result}
      </span>
      {checks.length > 0 && (
        <button className="btn btn-sm" onClick={() => setOpen(true)}>
          Clear outstanding checks
        </button>
      )}
      {open && (
        <ClearChecks
          project={project}
          count={checks.length}
          refresh={refresh}
          close={() => setOpen(false)}
          onCleared={(count) =>
            setResult(`Skipped ${count} ${checkWord(count)}.`)
          }
        />
      )}
    </>
  );
}
function ClearChecks({
  project,
  count,
  refresh,
  close,
  onCleared,
}: {
  project: string;
  count: number;
  refresh: () => Promise<void>;
  close: () => void;
  onCleared: (count: number) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const field = useRef<HTMLTextAreaElement>(null);
  const [note, setNote] = useState("");
  const hint = useId();
  const { busy, error, run } = useAction();
  useEffect(() => {
    const el = dialog.current;
    el?.showModal();
    field.current?.focus();
    return () => el?.close();
  }, []);
  return (
    <dialog
      ref={dialog}
      className="dialog"
      onCancel={(e) => {
        e.preventDefault();
        if (!busy) close();
      }}
      aria-labelledby="clear-checks-title"
    >
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          if (!note.trim()) return;
          void run(async () => {
            const { cleared } = await clearOwnerChecks(project, note);
            onCleared(cleared);
            await refresh();
            close();
          });
        }}
      >
        <h2 id="clear-checks-title">
          Clear {count} outstanding {checkWord(count)}?
        </h2>
        <p>Marks these checks skipped. Completed checks stay done.</p>
        <label>
          Why are you clearing them?
          <textarea
            className="field"
            ref={field}
            required
            maxLength={500}
            aria-describedby={hint}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </label>
        <p id={hint} className="hint" aria-live="polite">
          {!note.trim()
            ? "A short note is required to skip these checks."
            : "This note will be recorded with each skipped check."}
        </p>
        <ErrorNotice error={error} />
        <button type="button" className="btn" disabled={busy} onClick={close}>
          Cancel
        </button>
        <button className="btn" disabled={busy || !note.trim()}>
          Skip {count} {checkWord(count)}
        </button>
      </form>
    </dialog>
  );
}
