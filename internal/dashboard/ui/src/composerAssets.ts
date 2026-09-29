/**
 * Files dropped onto or pasted into the chat composer.
 *
 * The daemon accepts one text message per turn, so an asset travels inside
 * that message: each pending file is read here, checked, and written into the
 * message as a labelled, fenced block when it is sent. Only UTF-8 text files
 * can travel that way. Images, PDFs and other binary files are refused with a
 * reason rather than dropped, until the daemon has a transport for them.
 */

import { useState, type ClipboardEvent, type DragEvent } from "react";

/** Mirrors the byte limit core.EnqueueChat applies to a whole message. */
export const MESSAGE_LIMIT_BYTES = 24_000;

export type ComposerAsset = {
  id: string;
  name: string;
  size: number;
  content: string;
};

const textTypes = new Set([
  "application/json",
  "application/xml",
  "application/yaml",
  "application/x-yaml",
  "application/toml",
  "application/javascript",
  "application/x-sh",
]);

const textExtensions = new Set(
  (
    "txt md markdown csv tsv json yaml yml toml xml html css log ini conf " +
    "go ts tsx js jsx mjs py rb rs java kt swift c h cpp sh zsh fish sql"
  ).split(" "),
);

const bytes = (text: string) => new TextEncoder().encode(text).length;

export function sizeLabel(size: number) {
  if (size < 1000) return `${size} B`;
  if (size < 1_000_000) return `${(size / 1000).toFixed(1)} KB`;
  return `${(size / 1_000_000).toFixed(1)} MB`;
}

/** Limits are reported exactly; a rounded size can read as within the limit. */
export const exactSize = (size: number) =>
  `${size.toLocaleString("en-US")} bytes`;

function looksLikeText(file: File) {
  if (file.type.startsWith("text/") || textTypes.has(file.type)) return true;
  const dot = file.name.lastIndexOf(".");
  const extension = dot >= 0 ? file.name.slice(dot + 1).toLowerCase() : "";
  // Browsers leave the type empty for many plain-text formats.
  return (
    (!file.type || file.type === "application/octet-stream") &&
    textExtensions.has(extension)
  );
}

/**
 * Reads one file into a pending asset, or explains why it cannot be sent.
 * A file is never partly accepted.
 */
export async function readAsset(
  file: File,
): Promise<{ asset: ComposerAsset } | { error: string }> {
  const name = file.name || "Pasted file";
  if (!looksLikeText(file))
    return {
      error: `${name} can't be attached: only text files can be sent (${file.type || "unknown type"} is not supported yet).`,
    };
  if (file.size > MESSAGE_LIMIT_BYTES)
    return {
      error: `${name} can't be attached: it is ${exactSize(file.size)}, and a message can carry at most ${exactSize(MESSAGE_LIMIT_BYTES)}.`,
    };
  let content: string;
  try {
    content = new TextDecoder("utf-8", { fatal: true }).decode(
      await file.arrayBuffer(),
    );
  } catch {
    return {
      error: `${name} can't be attached: it is not readable UTF-8 text.`,
    };
  }
  if (content.includes("\0"))
    return {
      error: `${name} can't be attached: it contains binary data.`,
    };
  return {
    asset: { id: crypto.randomUUID(), name, size: file.size, content },
  };
}

function fenced(asset: ComposerAsset) {
  const longest = Math.max(
    0,
    ...(asset.content.match(/`+/g) || []).map((run) => run.length),
  );
  const fence = "`".repeat(Math.max(3, longest + 1));
  const body = asset.content.endsWith("\n")
    ? asset.content
    : `${asset.content}\n`;
  return `Attached file: ${asset.name}\n${fence}\n${body}${fence}`;
}

/**
 * The message the daemon receives: the owner's words, then each asset in the
 * order it was added.
 */
export function composeMessage(text: string, assets: ComposerAsset[]) {
  return [text.trim(), ...assets.map(fenced)].filter(Boolean).join("\n\n");
}

/** Why a composed message cannot be sent, or an empty string if it can. */
export function messageLimitError(message: string) {
  const size = bytes(message);
  return size > MESSAGE_LIMIT_BYTES
    ? `This message and its attachments come to ${exactSize(size)}; the limit is ${exactSize(MESSAGE_LIMIT_BYTES)}. Remove an attachment or shorten the message.`
    : "";
}

/** A drag or clipboard carries files, not just text. */
export function carriesFiles(data: DataTransfer | null) {
  return !!data && Array.from(data.types || []).includes("Files");
}

/**
 * Files dropped onto a form or pasted into its text field, handed to onFiles.
 * Dropped or pasted text still lands in the field as usual. Files on the same
 * clipboard as text (a copied file comes with its name as text) are still
 * handed over, so they are attached or refused with a reason, never dropped.
 */
export function useFileDrop(onFiles: (files: File[]) => void) {
  const [dragging, setDragging] = useState(false);
  const dropHandlers = {
    onDragOver(e: DragEvent<HTMLElement>) {
      if (!carriesFiles(e.dataTransfer)) return;
      e.preventDefault();
      setDragging(true);
    },
    onDragLeave(e: DragEvent<HTMLElement>) {
      const to = e.relatedTarget;
      if (!(to instanceof Node && e.currentTarget.contains(to)))
        setDragging(false);
    },
    onDrop(e: DragEvent<HTMLElement>) {
      setDragging(false);
      const files = Array.from(e.dataTransfer?.files || []);
      if (!files.length) return;
      e.preventDefault();
      onFiles(files);
    },
  };
  function onPaste(e: ClipboardEvent<HTMLElement>) {
    const files = Array.from(e.clipboardData.files || []);
    if (!files.length) return;
    if (!e.clipboardData.getData("text/plain")) e.preventDefault();
    onFiles(files);
  }
  return { dragging, dropHandlers, onPaste };
}
