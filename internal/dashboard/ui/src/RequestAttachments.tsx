import { sinceLabel } from "./ui";
import { sizeLabel } from "./composerAssets";
import { isImage, TASK_ATTACHMENTS } from "./attachments";
import {
  attachmentURL,
  type Attachment,
  type DesignRequest,
  type Task,
} from "./api";

/** Where a design stands: the target, superseded by it, or advice. */
export type DesignState = "current" | "superseded" | "advice";

export function designState(
  task: Task,
  r: DesignRequest,
): DesignState | undefined {
  if (!r.input) return undefined;
  if (r.id === task.current_design) return "current";
  return r.marked ? "superseded" : "advice";
}

export const designStateLabel: Record<DesignState, string> = {
  current: "Current design",
  superseded: "Superseded",
  advice: "Advice",
};

/** A task's files, pictures as thumbnails and the rest as links. */
export function AttachmentList({
  task,
  attachments,
  context,
}: {
  task: Task;
  attachments: Attachment[];
  /** Says what each file came with, where the list mixes them. */
  context?: (a: Attachment) => string;
}) {
  if (!attachments.length) return null;
  return (
    <ul className="attachment-files" aria-label="Attached files">
      {attachments.map((a) => {
        const url = attachmentURL(task.project_id, task.id, a.id);
        return (
          <li key={a.id}>
            {isImage(a.type) && (
              <a href={url} target="_blank" rel="noreferrer" tabIndex={-1}>
                <img
                  className="attachment-thumb"
                  src={url}
                  alt={a.name}
                  loading="lazy"
                />
              </a>
            )}
            <span className="attachment-line">
              <a href={url} target="_blank" rel="noreferrer">
                {a.name}
              </a>
              <span className="muted small">{sizeLabel(a.size)}</span>
              {context && <span className="muted small">{context(a)}</span>}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/**
 * Every file kept with the request, in one place, each saying what it came
 * with: a note, or a design and whether that design is current.
 */
export function RequestAttachments({ task }: { task: Task }) {
  const attachments = task.attachments ?? [];
  if (!attachments.length) return null;
  const notes = task.notes ?? [];
  const designs = task.design ?? [];
  function context(a: Attachment) {
    const when = a.at ? ` · ${sinceLabel(a.at)}` : "";
    const note = a.note && notes.find((n) => n.id === a.note);
    if (note)
      return `With ${note.kind === "owner" ? "your" : `${note.by}'s`} note${when}`;
    const design = a.design && designs.find((r) => r.id === a.design);
    if (design) {
      const state = designState(task, design);
      const label = design.n ? `Design ${design.n}` : "Design input";
      return `${label}${state ? ` · ${designStateLabel[state]}` : ""}${when}`;
    }
    const verdict =
      a.verdict && (task.verdicts ?? []).find((v) => v.id === a.verdict);
    if (verdict)
      return `${verdict.role}'s screenshot, checking draft ${verdict.revision}${when}`;
    return `Added by ${a.by}${when}`;
  }
  return (
    <section className="section" aria-label="Attachments">
      <h3>Attachments</h3>
      <AttachmentList task={task} attachments={attachments} context={context} />
      <p className="muted small">
        {attachments.length} of {TASK_ATTACHMENTS} files this request can keep
      </p>
    </section>
  );
}
