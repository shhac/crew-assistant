import { counted, recordedTime } from "./ui";
import { roleAtWork } from "./members";
import { needsYou, seatWords } from "./stages";
import type { Role, Stage, State, Task, Turn } from "./api";

/** Past this long without a word from its session, a turn may have stalled. */
export const quietAfter = 2 * 60_000;

/** The lanes where a request is with a role rather than the owner or the list. */
const workingStages = new Set<Stage>([
  "researching",
  "designing",
  "implementing",
  "reviewing",
  "qa",
]);

/** "45s", "12m", "1h 5m". */
export function span(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  return minutes % 60 ? `${hours}h ${minutes % 60}m` : `${hours}h`;
}

const since = (at: string, now: number) =>
  now - (recordedTime(at)?.valueOf() ?? now);

export const turnFor = (task: Task, turns: Turn[]) =>
  turns.find((t) => t.task_id === task.id);

export const isQuiet = (turn: Turn, now: number) =>
  since(turn.last_activity_at, now) > quietAfter;

/**
 * How a running turn is getting on: how long, how much it has done, and
 * when its session last said anything. Short words fit a board card.
 */
export function workingParts(turn: Turn, now: number, short: boolean) {
  const quiet = since(turn.last_activity_at, now);
  const files = turn.files_changed;
  return [
    "Working",
    span(since(turn.started_at, now)),
    counted(turn.tool_calls, short ? "call" : "tool call"),
    ...(files === undefined
      ? []
      : [short ? counted(files, "file") : `${counted(files, "file")} changed`]),
    quiet > quietAfter
      ? `quiet for ${span(quiet)}`
      : `active ${span(quiet)} ago`,
  ];
}

/**
 * Why a request in a working lane has not been picked up, when that can be
 * told. A request held for usage already says so in its step.
 */
function waitReason(
  task: Task,
  role: Role | undefined,
  state: State,
  now: number,
) {
  if (state.stopping) return "crew-assistant is stopping";
  if (state.paused) return "teams are paused";
  const running = state.turns[0];
  if (running) {
    if (!running.task_id) return `${running.seat} is ordering the to-do list`;
    const same = !!role?.member && running.member === role.member;
    const objective =
      state.tasks.find((t) => t.id === running.task_id)?.objective ??
      "another request";
    return `${same ? role.name : "the team"} is on “${objective}”`;
  }
  const until = recordedTime(task.retry_at);
  if (until && until.valueOf() > now && !task.detail)
    return `tries again at ${until.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    })}`;
  return "";
}

/**
 * "Waiting for Ada to pick this up", with the reason when there is one, for
 * a request that is with a role but has no turn running; "" otherwise.
 */
export function waitingLine(task: Task, state: State, now: number) {
  if (
    !workingStages.has(task.stage) ||
    task.status === "waiting" ||
    needsYou(task) ||
    turnFor(task, state.turns)
  )
    return "";
  const role = roleAtWork(task);
  const reason = waitReason(task, role, state, now);
  const who = role ? seatWords(task, role.name) : "the team";
  return `Waiting for ${who} to pick this up${reason ? ` · ${reason}` : ""}`;
}
