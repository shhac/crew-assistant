import type { ChatTurn } from "./api";
import type { ComposerAsset } from "./composerAssets";

export type VisibleTurn = Omit<ChatTurn, "status"> & {
  status:
    ChatTurn["status"] | "waiting" | "sending" | "unconfirmed" | "rejected";
  /** What the composer held, so a refused message restores its attachments. */
  draft?: { text: string; assets: ComposerAsset[] };
};
// A turn has spoken once its reply is in the thread; until then its newest
// tool step is still the most recent thing the owner has to look at.
export const turnLive = (turn?: VisibleTurn) =>
  turn?.status === "queued" || turn?.status === "running";
export const active = (turn: VisibleTurn) =>
  ["waiting", "sending", "queued", "running"].includes(turn.status);
// A turn in any of these states means the conversation is not waiting on the
// owner: a message is on its way, being answered, or needs their recovery.
export const unsettled = (turn: VisibleTurn) =>
  active(turn) || turn.status === "unconfirmed" || turn.status === "rejected";
export function chronological(
  a: { created_at?: string },
  b: { created_at?: string },
) {
  return (
    (Date.parse(a.created_at || "") || 0) -
    (Date.parse(b.created_at || "") || 0)
  );
}
const turnRank: Partial<Record<VisibleTurn["status"], number>> = {
  waiting: 0,
  sending: 0,
  unconfirmed: 0,
  rejected: 0,
  queued: 1,
  running: 2,
};
const rank = (status: VisibleTurn["status"]) => turnRank[status] ?? 3;
export function latestTurn(
  current: VisibleTurn | undefined,
  incoming: ChatTurn,
): VisibleTurn {
  // A poll begun before an acknowledgement must not undo its newer status.
  return current && rank(current.status) > rank(incoming.status)
    ? current
    : incoming;
}

/**
 * What a wake-up says happened, for the owner. The message itself is written
 * for the assistant: it opens with a note to the model and lists each wake's
 * details line by line.
 */
export function wakeSummary(content: string) {
  const happened = wakeHappenings(content);
  return happened.length ? happened.join(" · ") : "Checked in";
}

/** What each wake-up in the message saw happen, and nothing meant for the model. */
export function wakeHappenings(content: string) {
  return [...content.matchAll(/what happened: (.+)/g)].map((m) => m[1]);
}

/**
 * The command a message is, such as "compact", when the whole message is a
 * slash command; the daemon decides whether it knows it.
 */
export function commandIn(text: string | undefined) {
  // A turn read back without its text is not a command, and must not stop
  // the chat from rendering.
  return /^\/([A-Za-z][A-Za-z0-9_-]*)$/
    .exec((text ?? "").trim())?.[1]
    .toLowerCase();
}

/** What a command does, until it says what it did. */
export function commandLabel(command: string) {
  switch (command) {
    case "compact":
      return "Summarize the conversation so far";
    case "new":
    case "clear":
      return "Start a fresh conversation";
  }
  return "Command";
}

/** A message's delivery, in a few words; nothing once all is well. */
export function delivery(turn: VisibleTurn) {
  switch (turn.status) {
    case "waiting":
      return "Waiting to send";
    case "sending":
      return "Sending…";
    case "unconfirmed":
      return "Not confirmed yet";
    case "rejected":
      return "Not sent";
    case "queued":
      return "Queued";
    case "cancelled":
      return "Cancelled";
    case "interrupted":
      return "Stopped before finishing. Not retried.";
    case "failed":
      return "Failed. Not retried.";
  }
  return "";
}
