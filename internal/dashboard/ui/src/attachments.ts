/**
 * Files the owner attaches to a note. Unlike the chat composer's assets,
 * these travel as files, not inside the message, so images and PDFs are
 * taken too. The daemon judges every file by its bytes; the checks here
 * mirror its limits so a file is refused with the same reason before it is
 * sent, and never partly accepted.
 */
import { exactSize } from "./composerAssets";

/** Mirror core.MaxAttachmentBytes, MaxAttachmentsPerSet and MaxTaskAttachments. */
export const ATTACHMENT_LIMIT_BYTES = 5 * 1024 * 1024;
export const ATTACHMENTS_PER_NOTE = 10;
export const TASK_ATTACHMENTS = 120;

/** Mirrors core's attachment types, by extension. */
const extensions = new Set(
  "png jpg jpeg gif webp pdf md markdown txt json csv svg html htm".split(" "),
);

export const ATTACHMENT_KINDS =
  "images (PNG, JPEG, GIF, WebP), PDF, and text files (Markdown, plain text, JSON, CSV, SVG, HTML)";

/** Accept list for the file picker. */
export const ATTACHMENT_ACCEPT = [...extensions].map((e) => `.${e}`).join(",");

export type PendingFile = { id: string; file: File };

/** Why a file can't be attached to a note, or an empty string if it can. */
export function attachmentError(file: File, pending: number) {
  const name = file.name || "Pasted file";
  const dot = name.lastIndexOf(".");
  const extension = dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
  if (!extensions.has(extension))
    return `${name} can't be attached: only ${ATTACHMENT_KINDS} can be attached.`;
  if (file.size === 0) return `${name} can't be attached: it is empty.`;
  if (file.size > ATTACHMENT_LIMIT_BYTES)
    return `${name} can't be attached: it is ${exactSize(file.size)}, and a file can be at most ${exactSize(ATTACHMENT_LIMIT_BYTES)}.`;
  if (pending >= ATTACHMENTS_PER_NOTE)
    return `${name} can't be attached: a note can have at most ${ATTACHMENTS_PER_NOTE} files.`;
  return "";
}

/** Takes the files that can be attached, and says why the rest can't. */
export function addPending(current: PendingFile[], files: File[]) {
  const added: PendingFile[] = [];
  const refused: string[] = [];
  for (const file of files) {
    const error = attachmentError(file, current.length + added.length);
    if (error) refused.push(error);
    else added.push({ id: crypto.randomUUID(), file });
  }
  return { pending: [...current, ...added], refused };
}

/** An attachment shown as a picture rather than a link. */
export function isImage(type: string) {
  return type.startsWith("image/");
}
