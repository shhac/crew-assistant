import { useState, type ReactNode } from "react";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { kindLabel } from "./members";
import { sinceLabel } from "./ui";
import type { TeamMessage, TurnStep } from "./api";

/** Past this, a reply shows its opening until the owner asks for the rest. */
const replyFold = 700;

type Entry =
  | { at: string; step: TurnStep; message?: undefined }
  | { at: string; message: TeamMessage; step?: undefined };

/**
 * Steps and messages as one conversation, oldest first so the newest sits
 * by the box. A message with no time goes last.
 */
export function Timeline({
  steps,
  messages,
  seat,
  loaded,
  renderMessage,
}: {
  steps: TurnStep[];
  messages: TeamMessage[];
  seat: string;
  loaded: boolean;
  renderMessage: (m: TeamMessage) => ReactNode;
}) {
  const entries: Entry[] = [
    ...steps.map((step) => ({ at: step.at, step })),
    ...messages.map((message) => ({ at: message.at ?? "", message })),
  ];
  const time = (at: string) => Date.parse(at) || Number.MAX_SAFE_INTEGER;
  entries.sort((a, b) => time(a.at) - time(b.at));
  if (!entries.length)
    return (
      <p className="muted small">
        {loaded
          ? `Nothing from ${seat} on this request yet.`
          : "Loading what they've done…"}
      </p>
    );
  return (
    <ol className="member-timeline" aria-label="Activity">
      {entries.map((e) =>
        e.message ? (
          renderMessage(e.message)
        ) : (
          <StepView key={`step-${e.step.id}`} step={e.step} seat={seat} />
        ),
      )}
    </ol>
  );
}

function StepView({ step, seat }: { step: TurnStep; seat: string }) {
  const when = sinceLabel(step.at);
  if (step.kind === "prompt")
    return (
      <li className="step step-prompt">
        <details>
          <summary>
            <span className="step-label">Prompt</span>
            <span className="muted small">
              {kindLabel(step.role)} · {when}
            </span>
          </summary>
          <pre className="step-pre">{step.text}</pre>
          {step.clipped && <Clipped />}
        </details>
      </li>
    );
  if (step.kind === "reply")
    return (
      <li className="step step-reply">
        <p className="thread-line">
          <span className="thread-who">{seat}</span>
          <span className="muted small">{when}</span>
        </p>
        <Folded text={step.text ?? ""} />
        {step.clipped && <Clipped />}
      </li>
    );
  const status = step.status ?? "completed";
  const mark = status === "completed" ? "✓" : status === "running" ? "•" : "!";
  return (
    <li className={`step step-tool tool-${status}`}>
      <details>
        <summary>
          <span className="tool-mark" aria-hidden="true">
            {mark}
          </span>
          <code>{step.tool || "A tool"}</code>
          <span className="step-gist">{gist(step.input)}</span>
          <span className="muted small">{toolOutcome(step)}</span>
        </summary>
        <div className="step-detail">
          {step.input && (
            <>
              <p className="label">Input</p>
              <pre className="step-pre">{pretty(step.input)}</pre>
            </>
          )}
          {step.output ? (
            <>
              <p className="label">Output</p>
              <pre className="step-pre">{step.output}</pre>
            </>
          ) : (
            status !== "running" && <p className="muted small">No output.</p>
          )}
          {step.clipped && <Clipped />}
        </div>
      </details>
    </li>
  );
}

const Clipped = () => (
  <p className="muted small">Cut to its first part; the rest wasn't kept.</p>
);

/** "Failed · exit 1", "Running", "Stopped before it finished". */
function toolOutcome(step: TurnStep) {
  const words: Record<string, string> = {
    running: "Running",
    completed: "Done",
    failed: "Failed",
    interrupted: "Stopped before it finished",
  };
  const status = step.status ?? "completed";
  const said = words[status] ?? status;
  return step.exit_code !== undefined && step.exit_code !== 0
    ? `${said} · exit ${step.exit_code}`
    : said;
}

/** The part of a tool's input that says what it did: its command or path. */
function gist(input?: string) {
  if (!input) return "";
  const line = inputLine(parsed(input)).replace(/\s+/g, " ").trim();
  return line.length > 90 ? `${line.slice(0, 90)}…` : line;
}

function parsed(input: string): unknown {
  try {
    return JSON.parse(input);
  } catch {
    // Cut short, so no longer JSON; its opening still says something.
    return input;
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === "object";

function inputLine(value: unknown) {
  if (typeof value === "string") return value;
  if (!isRecord(value)) return "";
  const key = [
    "command",
    "cmd",
    "file_path",
    "path",
    "pattern",
    "query",
    "url",
    "description",
  ].find((k) => typeof value[k] === "string");
  const first = Object.values(value).find((v) => typeof v === "string");
  return String(key ? value[key] : (first ?? ""));
}

function pretty(input: string) {
  try {
    return JSON.stringify(JSON.parse(input), null, 2);
  } catch {
    return input;
  }
}

/** A reply in full when it is short, and its opening until asked otherwise. */
function Folded({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > replyFold;
  return (
    <div className="step-text">
      <ConversationMarkdown
        content={
          long && !open ? `${text.slice(0, replyFold).trimEnd()}…` : text
        }
      />
      {long && (
        <button
          type="button"
          className="link-button small"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  );
}
