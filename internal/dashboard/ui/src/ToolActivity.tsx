import type { ChatToolEvent } from "./api";

function seconds(from?: string, to?: string) {
  const start = Date.parse(from || "");
  const end = Date.parse(to || "");
  return Number.isNaN(start) || Number.isNaN(end)
    ? 0
    : Math.max(0, (end - start) / 1000);
}

export function durationLabel(total: number) {
  const whole = Math.round(total);
  if (whole < 1) return "";
  if (whole < 60) return `${whole} s`;
  const minutes = Math.floor(whole / 60);
  const rest = whole % 60;
  return rest ? `${minutes} min ${rest} s` : `${minutes} min`;
}

const troubled = (e: ChatToolEvent) =>
  e.status === "failed" || e.status === "interrupted";

/**
 * A turn's steps as one line: what it is doing while it works, and how long
 * it took once it is done. Steps that failed, or whose outcome is unknown,
 * are always shown; the rest fold away.
 */
export function ToolActivity({
  events,
  live = false,
}: {
  events: ChatToolEvent[];
  /** This turn is the most recent thing in the thread and has not replied. */
  live?: boolean;
}) {
  if (!events.length) return null;
  const running = events.find((e) => e.status === "running");
  const problems = events.filter(troubled);
  const failed = events.filter((e) => e.status === "failed").length;
  const unconfirmed = events.filter((e) => e.status === "interrupted").length;
  const took = durationLabel(
    seconds(
      events[0]?.started_at,
      events.at(-1)?.finished_at ?? events.at(-1)?.started_at,
    ),
  );
  const count = `${events.length} ${events.length === 1 ? "step" : "steps"}`;
  const summary = running
    ? `${running.label || "Working"}…`
    : live
      ? events.at(-1)?.label || "Working"
      : [
          took ? `Worked for ${took}` : "Worked",
          count,
          failed ? `${failed} failed` : "",
          unconfirmed ? `${unconfirmed} not confirmed` : "",
        ]
          .filter(Boolean)
          .join(" · ");
  return (
    <div className="tools" role="group" aria-label="What the assistant did">
      <details className="tools-summary">
        <summary>{summary}</summary>
        <ul>
          {events.map((event) => (
            <ToolRow key={event.id} event={event} />
          ))}
        </ul>
      </details>
      {problems.length > 0 && (
        <ul className="tools-problems">
          {problems.map((event) => (
            <ToolRow key={event.id} event={event} />
          ))}
        </ul>
      )}
    </div>
  );
}

const outcome: Record<ChatToolEvent["status"], string> = {
  running: "Working",
  completed: "Done",
  interrupted: "Stopped; outcome not confirmed",
  failed: "Failed",
};

function ToolRow({ event }: { event: ChatToolEvent }) {
  return (
    <li className={`tool tool-${event.status}`}>
      <span className="tool-mark" aria-hidden="true">
        {event.status === "completed"
          ? "✓"
          : event.status === "running"
            ? "•"
            : "!"}
      </span>
      <span>
        {event.label || "A step"}
        {event.status !== "completed" && (
          <span className="muted small"> · {outcome[event.status]}</span>
        )}
      </span>
    </li>
  );
}
