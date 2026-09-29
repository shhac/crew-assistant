import type { ChatSession } from "./api";
import { engineLabel } from "./engines";
import { commandLabel } from "./chatTurns";

const percent = (part: number, whole: number) =>
  `${Math.round((part / whole) * 100)}%`;

/** The session in a line, leaving out whatever it has not reported. */
function sessionParts(session: ChatSession) {
  const parts = [engineLabel(session.engine), session.model];
  if (session.context_used && session.context_window)
    parts.push(
      `${percent(session.context_used, session.context_window)} of context`,
    );
  if (session.input)
    parts.push(`${percent(session.cached_input ?? 0, session.input)} cached`);
  if (session.compactions) parts.push(`compacted ${session.compactions}×`);
  return parts.filter(Boolean);
}

function sessionOpened(session: ChatSession) {
  switch (session.opened) {
    case "resumed":
      return "Picked up where it left off";
    case "rebuilt":
      return "Started afresh: the last session couldn't be resumed";
  }
  return "";
}

const sessionCommands = [
  { command: "compact", label: "Compact" },
  { command: "new", label: "Start fresh" },
];

/**
 * The model session the conversation runs on, with the commands that act on
 * it. Both wait while anything is still being sent or answered.
 */
export function SessionLine({
  session,
  busy,
  onCommand,
}: {
  session: ChatSession;
  busy: boolean;
  onCommand: (command: string) => void;
}) {
  const line = sessionParts(session).join(" · ");
  const opened = sessionOpened(session);
  return (
    <div className="chat-session" role="group" aria-label="Model session">
      <p className="chat-session-about">
        <span className="chat-session-line">{line}</span>
        {opened && <span className="chat-session-opened">{opened}</span>}
      </p>
      {sessionCommands.map(({ command, label }) => (
        <button
          key={command}
          type="button"
          className="btn btn-quiet btn-sm"
          disabled={busy}
          title={`${commandLabel(command)} (/${command})`}
          onClick={() => onCommand(command)}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
