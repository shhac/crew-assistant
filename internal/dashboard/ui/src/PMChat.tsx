import {
  useEffect,
  useRef,
  useState,
  type ReactNode,
  type FormEvent,
} from "react";
import { Avatar } from "./Avatar";
import { Composer, LargeEditor, focusDraft } from "./Composer";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { pmSeat } from "./members";
import { projectHref, requestHref } from "./router";
import { dateLabel } from "./ui";
import {
  errorText,
  getPMChat,
  sendPMChat,
  retryPMChat,
  type Project,
  type State,
  type PMChatMessage,
  type PMChatChange,
} from "./api";

export function NoPM({ project }: { project: Project }) {
  return (
    <p className="soft">
      This project has no PM yet. Add one to its team to discuss the order of
      work. <a href={projectHref(project.id, "team")}>Open Team</a>
    </p>
  );
}
export function PMChat({
  project,
  state,
  draft,
  onDraft,
}: {
  project: Project;
  state: State;
  draft?: string;
  onDraft?: (value: string) => void;
}) {
  const seat = pmSeat(project)!;
  const member = state.members.find((m) => m.id === seat.member);
  const [preset, setPreset] = useState<{ name: string; avatar_svg?: string }>();
  const face = member ?? (preset?.name === seat.name ? preset : {});
  const [local, setLocal] = useState("");
  const value = draft ?? local;
  const setDraft = onDraft ?? setLocal;
  const draftRef = useRef(value);
  draftRef.current = value;
  const [messages, setMessages] = useState<PMChatMessage[]>([]);
  const [error, setError] = useState("");
  const [large, setLarge] = useState(false);
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  const retrying = useRef(new Set<string>());
  const [retryBusy, setRetryBusy] = useState(new Set<string>());
  const generation = useRef(0);
  const log = useRef<HTMLDivElement>(null);
  const logVersion = JSON.stringify(messages);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [project.id, logVersion]);
  const pending = messages.some(
    (m) => m.status === "waiting" || m.status === "working",
  );
  const id = `pm-message-${project.id}`;
  const previousLarge = useRef(false);
  useEffect(() => {
    if (previousLarge.current && !large) focusDraft(id);
    previousLarge.current = large;
  }, [large, id]);
  useEffect(() => {
    focusDraft(id);
  }, [id]);
  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      const g = ++generation.current;
      try {
        const result = await getPMChat(project.id);
        if (!cancelled && g === generation.current) {
          setMessages(result.messages);
          setPreset({ name: seat.name, avatar_svg: result.avatar_svg });
          setError("");
        }
      } catch (e) {
        if (!cancelled && g === generation.current) setError(errorText(e));
      }
    };
    void load();
    const timer = pending
      ? window.setInterval(() => void load(), 2000)
      : undefined;
    return () => {
      cancelled = true;
      if (timer) window.clearInterval(timer);
    };
  }, [project.id, seat.name, pending]);
  const attempt = useRef<{ id: string; text: string } | null>(null);
  async function send(e: FormEvent) {
    e.preventDefault();
    const text = draftRef.current;
    if (!text.trim() || sending.current) return;
    sending.current = true;
    setBusy(true);
    setError("");
    if (!attempt.current || attempt.current.text !== text)
      attempt.current = { id: crypto.randomUUID(), text };
    try {
      const m = await sendPMChat(project.id, attempt.current.id, text);
      generation.current++;
      setMessages((prior) =>
        prior.some((o) => o.id === m.id) ? prior : [...prior, m],
      );
      if (draftRef.current === text) setDraft("");
      attempt.current = null;
      setLarge(false);
    } catch (e) {
      setError(errorText(e));
    } finally {
      sending.current = false;
      setBusy(false);
    }
  }
  async function retry(m: PMChatMessage) {
    if (retrying.current.has(m.id)) return;
    retrying.current.add(m.id);
    setRetryBusy(new Set(retrying.current));
    try {
      const updated = await retryPMChat(project.id, m.id);
      generation.current++;
      setMessages((prior) => prior.map((o) => (o.id === m.id ? updated : o)));
      setError("");
    } catch (e) {
      setError(errorText(e));
    } finally {
      retrying.current.delete(m.id);
      setRetryBusy(new Set(retrying.current));
    }
  }
  const composer = {
    id,
    name: seat.name,
    placeholder: `Message ${seat.name} about ${project.title}`,
    textOnly: true,
    maxLength: 24000,
    value,
    onChange: setDraft,
    onSubmit: send,
    onExpand: () => setLarge(true),
    reading: busy ? 1 : 0,
  };
  return (
    <section className="pm-chat" aria-label="PM chat">
      <header className="pm-chat-header">
        <Avatar of={face} size={40} />
        <div>
          <h2>{seat.name}</h2>
          <p className="muted">PM · {project.title}</p>
          {!messages.length && (
            <p className="soft">
              I keep this project's to-do list and dependencies in order.
            </p>
          )}
        </div>
      </header>
      <div
        ref={log}
        className="chat-log pm-chat-log"
        role="log"
        aria-label="Project PM conversation"
      >
        {!messages.length && (
          <p className="chat-welcome soft">
            Ask {seat.name} about the order of work, what should wait, or what
            needs your decision.
          </p>
        )}
        {messages.map((m) => (
          <article
            key={m.id}
            className={`message ${m.from === "owner" ? "from-you" : "from-assistant"}`}
          >
            <header className="message-by">
              {m.from === "pm" && <Avatar of={face} size={24} />}
              <strong>
                {m.from === "owner" ? "You" : `${m.by || seat.name} · PM`}
              </strong>
              <time dateTime={m.at}>{dateLabel(m.at)}</time>
            </header>
            <div className="message-body">
              <ConversationMarkdown content={m.text} />
            </div>
            {m.status === "waiting" && (
              <p className="turn-delivery">Waiting for {m.by || seat.name}…</p>
            )}
            {m.status === "working" && (
              <Running message={m} name={m.by || seat.name} />
            )}
            {m.status === "failed" && (
              <div className="turn-recovery">
                <p className="error">
                  {m.by || seat.name} couldn’t reply. Your message is still
                  here.
                </p>
                {m.error && <p className="muted small">{m.error}</p>}
                <button
                  className="btn btn-sm"
                  disabled={retryBusy.has(m.id)}
                  onClick={() => void retry(m)}
                >
                  Retry
                </button>
              </div>
            )}
            <Receipt
              changes={m.changes || []}
              project={project}
              state={state}
            />
          </article>
        ))}
      </div>
      <div className="chat-foot">
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {!large && <Composer {...composer} />}
      </div>
      {large && (
        <LargeEditor id={id} onDone={() => setLarge(false)}>
          <Composer {...composer} large />
        </LargeEditor>
      )}
    </section>
  );
}
function Running({ message, name }: { message: PMChatMessage; name: string }) {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const seconds = message.started_at
    ? Math.max(0, Math.floor((now - Date.parse(message.started_at)) / 1000))
    : null;
  return (
    <p className="turn-delivery pm-chat-running">
      {name} is looking at the project…{" "}
      {seconds !== null && <span className="muted small">{seconds}s</span>}
    </p>
  );
}
function Receipt({
  changes,
  project,
  state,
}: {
  changes: PMChatChange[];
  project: Project;
  state: State;
}) {
  if (!changes.length) return null;
  const labels = {
    reordered: "Reordered to-do list",
    updated: "Updated requests",
    linked: "Linked requests",
    queued: "Queued requests",
  };
  const item = (c: PMChatChange, i: number) => (
    <li key={i}>{linkedSummary(c, project, state)}</li>
  );
  return (
    <section className="pm-chat-receipt" aria-label="Changes made">
      <strong>Changes made</strong>
      {(Object.keys(labels) as (keyof typeof labels)[]).map((kind) => {
        const items = changes.filter((c) => c.kind === kind);
        if (!items.length) return null;
        return items.length > 3 ? (
          <details key={kind}>
            <summary>
              {labels[kind]} ({items.length})
            </summary>
            <ul>{items.map(item)}</ul>
          </details>
        ) : (
          <ul key={kind}>{items.map(item)}</ul>
        );
      })}
      <a href={projectHref(project.id)}>View on board</a>
    </section>
  );
}

// Link request names inside the committed summary rather than repeating them.
function linkedSummary(
  c: PMChatChange,
  project: Project,
  state: State,
): ReactNode {
  if (c.kind === "reordered") return c.summary;
  const tasks = c.tasks
    .map((id) => state.tasks.find((t) => t.id === id))
    .filter((t) => t !== undefined);
  const parts: ReactNode[] = [];
  const linked = new Set<string>();
  let remaining = c.summary;
  while (remaining) {
    const matches = tasks
      .map((t) => ({ task: t, index: remaining.indexOf(t.objective) }))
      .filter((m) => !linked.has(m.task.id) && m.task.objective && m.index >= 0)
      .sort(
        (a, b) =>
          a.index - b.index ||
          b.task.objective.length - a.task.objective.length,
      );
    const match = matches[0];
    if (!match) {
      parts.push(remaining);
      break;
    }
    linked.add(match.task.id);
    parts.push(remaining.slice(0, match.index));
    parts.push(
      <a key={parts.length} href={requestHref(project.id, match.task.id)}>
        {match.task.objective}
      </a>,
    );
    remaining = remaining.slice(match.index + match.task.objective.length);
  }
  for (const id of c.tasks.filter((id) => !linked.has(id))) {
    parts.push(
      " ",
      <a key={`missing-${id}`} href={requestHref(project.id, id)}>
        Open request
      </a>,
    );
  }
  return parts;
}
