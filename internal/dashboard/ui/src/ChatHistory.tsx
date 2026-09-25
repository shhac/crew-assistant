import { useEffect, useState } from "react";
import {
  errorText,
  getConversation,
  listConversations,
  resumeConversation,
  type Conversation,
  type ConversationEntry,
  type Project,
} from "./api";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { dateLabel } from "./ui";

/** Who a message in a past conversation is from, in a word or two. */
function byline(role: string, origin: string | undefined, name: string) {
  if (origin === "wake") return "Wake-up";
  if (role === "summary") return "Summary so far";
  return role === "user" ? "You" : name;
}

/**
 * Past conversations, archived by /new and /clear: listed newest first, one
 * opened to read, and picked up again to carry on where it was left.
 */
export function ChatHistory({
  name,
  projects,
  onProjectOpen,
  onResumed,
  onClose,
}: {
  name: string;
  projects: Project[];
  onProjectOpen?: (id: string) => void;
  onResumed: () => Promise<void>;
  onClose: () => void;
}) {
  const [list, setList] = useState<ConversationEntry[] | null>(null);
  const [open, setOpen] = useState<Conversation | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    listConversations()
      .then((reply) => setList(reply.conversations || []))
      .catch((err) => setError(errorText(err)));
  }, []);
  async function show(id: string) {
    setError("");
    try {
      setOpen(await getConversation(id));
    } catch (err) {
      setError(errorText(err));
    }
  }
  async function resume(id: string) {
    setBusy(true);
    setError("");
    try {
      await resumeConversation(id);
      await onResumed();
      onClose();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="chat-history" aria-label="Past conversations">
      <div className="chat-history-head">
        <button
          type="button"
          className="btn btn-quiet btn-sm"
          onClick={() => (open ? setOpen(null) : onClose())}
        >
          {open ? "All conversations" : "Back to the chat"}
        </button>
        {open && (
          <button
            type="button"
            className="btn btn-primary btn-sm"
            disabled={busy}
            onClick={() => void resume(open.id)}
          >
            Continue this conversation
          </button>
        )}
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {open ? (
        <div className="chat-history-thread">
          <h3>{open.title}</h3>
          {open.messages.map((m) => (
            <article
              key={m.id}
              className={`message ${m.role === "user" ? "from-you" : "from-assistant"}`}
            >
              <p className="message-by">
                <span>{byline(m.role, m.origin, name)}</span>
                {m.created_at && (
                  <time dateTime={m.created_at}>{dateLabel(m.created_at)}</time>
                )}
              </p>
              {m.origin !== "wake" && (
                <div className="message-body">
                  <ConversationMarkdown
                    content={m.content}
                    projects={projects}
                    onProjectOpen={onProjectOpen}
                  />
                </div>
              )}
            </article>
          ))}
        </div>
      ) : list === null ? (
        !error && <p className="muted small">Loading…</p>
      ) : list.length ? (
        <ul className="chat-history-list">
          {list.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                className="chat-history-item"
                onClick={() => void show(c.id)}
              >
                <span className="chat-history-title">{c.title}</span>
                <span className="muted small">
                  {dateLabel(c.started_at)} · {c.messages}{" "}
                  {c.messages === 1 ? "message" : "messages"}
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="muted small">
          No past conversations yet. /new or /clear starts a fresh one and
          keeps this one here.
        </p>
      )}
    </section>
  );
}
