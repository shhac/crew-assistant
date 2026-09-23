import { useEffect, useRef, useState, type FormEvent } from "react";
import { Avatar, Waiting } from "./Identity";
import {
  api,
  APIError,
  errorText,
  pendingDecisions,
  type ChatTurn,
  type State,
} from "./api";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { dateLabel, Icon } from "./ui";
import { ChatQueue, type QueueHold } from "./ChatQueue";
import { ToolActivity } from "./ToolActivity";
import { fullDateLabel } from "./ui";
import {
  carriesFiles,
  composeMessage,
  messageLimitError,
  readAsset,
  sizeLabel,
  type ComposerAsset,
} from "./composerAssets";
import "./chat.css";
type VisibleTurn = Omit<ChatTurn, "status"> & {
  status:
    ChatTurn["status"] | "waiting" | "sending" | "unconfirmed" | "rejected";
  /** What the composer held, so a refused message restores its attachments. */
  draft?: { text: string; assets: ComposerAsset[] };
};
// A turn has spoken once its reply is in the thread; until then its newest
// tool step is still the most recent thing the owner has to look at.
const turnReplied = (turn?: VisibleTurn) => !!turn?.assistant_message_id;
const active = (turn: VisibleTurn) =>
  ["waiting", "sending", "queued", "running"].includes(turn.status);
function chronological(a: { created_at?: string }, b: { created_at?: string }) {
  return (
    (Date.parse(a.created_at || "") || 0) -
    (Date.parse(b.created_at || "") || 0)
  );
}
function latestTurn(
  current: VisibleTurn | undefined,
  incoming: ChatTurn,
): VisibleTurn {
  const rank = (status: VisibleTurn["status"]) =>
    status === "waiting" ||
    status === "sending" ||
    status === "unconfirmed" ||
    status === "rejected"
      ? 0
      : status === "queued"
        ? 1
        : status === "running"
          ? 2
          : 3;
  // A poll begun before an acknowledgement must not undo its newer status.
  return current && rank(current.status) > rank(incoming.status)
    ? current
    : incoming;
}
function delivery(turn: VisibleTurn) {
  switch (turn.status) {
    case "waiting":
      return "Waiting to send · kept in this browser";
    case "sending":
      return "Sending…";
    case "unconfirmed":
      return "Delivery unconfirmed";
    case "rejected":
      return "Message not sent";
    case "queued":
      return "Queued · will follow the current reply";
    case "running":
      return "Received · working on your request";
    case "completed":
      return "Reply complete";
    case "cancelled":
      return "Cancelled before starting";
    case "interrupted":
      return "Reply interrupted · not automatically retried";
    case "failed":
      return "Reply failed · not automatically retried";
  }
}
/**
 * What happened to one owner message: its delivery state, any recovery the
 * owner can choose, the assistant's tool activity and its working indicator.
 * Rendered for a message that has a turn; absent when it has none.
 */
function TurnStatus({
  turn,
  live,
  name,
  cancelling,
  onCancel,
  onRestore,
  onDiscard,
  onRetry,
}: {
  turn?: VisibleTurn;
  /** Nothing in the thread is newer than this turn, and it has not replied. */
  live?: boolean;
  name: string;
  cancelling: Set<string>;
  onCancel: (turn: VisibleTurn) => void;
  onRestore: (turn: VisibleTurn) => void;
  onDiscard: (turn: VisibleTurn) => void;
  onRetry: (turn: VisibleTurn) => void;
}) {
  if (!turn) return null;
  return (
    <div className="chat-turn-status">
      <span className="message-delivery">{delivery(turn)}</span>
      {turn.status === "waiting" && (
        <button
          type="button"
          className="chat-turn-action"
          disabled={cancelling.has(turn.id)}
          onClick={() => void onCancel(turn)}
          aria-label={`Cancel queued message: ${turn.message}`}
        >
          Cancel
        </button>
      )}
      {turn.error && (
        <p className="chat-turn-error" role="alert">
          {turn.error}
        </p>
      )}
      {turn.status === "unconfirmed" && (
        <div className="chat-recovery">
          <p>
            Your message is kept here while delivery is checked. Retrying
            delivery cannot start a second reply.
          </p>
          <button type="button" onClick={() => void onRetry(turn)}>
            Retry delivery
          </button>
        </div>
      )}
      {turn.status === "rejected" && (
        <div className="chat-recovery">
          <button type="button" onClick={() => onRestore(turn)}>
            Restore message to draft
          </button>
          <button type="button" onClick={() => onDiscard(turn)}>
            Discard message
          </button>
        </div>
      )}
      {!!turn.events?.length && (
        <ToolActivity events={turn.events} live={live} />
      )}
      {turn.status === "running" && (
        <Waiting
          label={
            turn.model_status ||
            turn.loading_phrase ||
            `${name} is working through it…`
          }
          detail={
            fullDateLabel(turn.retry_at)
              ? `Next attempt after ${fullDateLabel(turn.retry_at)}. Recorded actions will not be replayed.`
              : "An answer or a clear decision is on its way."
          }
        />
      )}
    </div>
  );
}
export function ChatPanel({
  state,
  refresh,
  onClose,
  expanded,
  onExpand,
  onProjectOpen,
}: {
  state: State;
  refresh: () => Promise<void>;
  onClose: () => void;
  expanded: boolean;
  onExpand: () => void;
  onProjectOpen?: (id: string) => void;
}) {
  const [message, setMessage] = useState("");
  const draftRef = useRef("");
  function setDraft(value: string) {
    draftRef.current = value;
    setMessage(value);
  }
  const [assets, setAssets] = useState<ComposerAsset[]>([]);
  const assetsRef = useRef(assets);
  assetsRef.current = assets;
  const [assetErrors, setAssetErrors] = useState<string[]>([]);
  const [reading, setReading] = useState(0);
  const readingRef = useRef(0);
  const [dragging, setDragging] = useState(false);
  async function addFiles(files: File[]) {
    if (!files.length) return;
    readingRef.current += files.length;
    setReading(readingRef.current);
    const results = await Promise.all(files.map(readAsset));
    readingRef.current -= files.length;
    setReading(readingRef.current);
    const added = results.flatMap((r) => ("asset" in r ? [r.asset] : []));
    setAssets((current) => [...current, ...added]);
    setAssetErrors(results.flatMap((r) => ("error" in r ? [r.error] : [])));
  }
  const [turns, setTurns] = useState<VisibleTurn[]>([]);
  const [error, setError] = useState("");
  const [pollError, setPollError] = useState("");
  const [queue, setQueue] = useState<{
    hold?: QueueHold | null;
    revision: number;
  }>({ revision: 0 });
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const turnsRef = useRef(turns);
  turnsRef.current = turns;
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  const submitLocks = useRef(new Set<string>());
  const outgoing = useRef<VisibleTurn[]>([]);
  const delivering = useRef(false);
  const uncertainDelivery = useRef<string | null>(null);
  const confirmed = useRef(new Set<string>());
  const scroll = useRef<HTMLDivElement>(null);
  const name = state.assistant.name || "Assistant";
  const messageIDs = new Set(state.messages.map((m) => m.id));
  const messages = [
    ...state.messages,
    ...turns
      .filter(
        (t) =>
          t.status !== "queued" &&
          (!t.user_message_id || !messageIDs.has(t.user_message_id)),
      )
      .map((t) => ({
        id: t.user_message_id || t.id,
        role: "user",
        content: t.message,
        created_at: t.created_at,
      })),
  ].sort(chronological);
  // The last rendered message in the thread. Only its turn may hold a
  // completed step out of the accordion.
  const newestMessageID = messages[messages.length - 1]?.id;
  const turnsByMessage = new Map(
    turns.map((t) => [t.user_message_id || t.id, t]),
  );
  const running = turns.find((t) => t.status === "running");
  const eventSignature = turns
    .map(
      (t) =>
        `${t.id}:${t.status}:${t.events?.map((e) => `${e.id}:${e.status}`).join(",")}`,
    )
    .join("|");
  useEffect(() => {
    if (scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight;
  }, [messages.length, eventSignature]);
  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    let previousSignature = "";
    async function poll() {
      try {
        const result = await api<{
          turns: ChatTurn[];
          hold: QueueHold | null;
          revision: number;
        }>("/api/chat/turns");
        if (stopped) return;
        setQueue({ hold: result.hold, revision: result.revision });
        const incoming = result.turns || [];
        incoming.forEach((t) => confirmed.current.add(t.id));
        if (
          uncertainDelivery.current &&
          confirmed.current.has(uncertainDelivery.current)
        ) {
          uncertainDelivery.current = null;
          drainOutgoing();
        }
        const signature = incoming
          .map(
            (t) =>
              `${t.id}:${t.status}:${t.user_message_id}:${t.assistant_message_id}`,
          )
          .join("|");
        setTurns((current) => {
          const merged = new Map(current.map((t) => [t.id, t]));
          incoming.forEach((t) =>
            merged.set(t.id, latestTurn(merged.get(t.id), t)),
          );
          return [...merged.values()].sort(chronological);
        });
        setPollError("");
        if (signature !== previousSignature) {
          await refreshRef.current();
          previousSignature = signature;
        }
      } catch (err) {
        if (!stopped)
          setPollError(
            `Live conversation updates unavailable: ${errorText(err)}`,
          );
      } finally {
        if (!stopped)
          timer = setTimeout(poll, turnsRef.current.some(active) ? 1000 : 3000);
      }
    }
    void poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, []);
  function enqueue(turn: VisibleTurn) {
    if (submitLocks.current.has(turn.id)) return;
    submitLocks.current.add(turn.id);
    if (turn.status === "unconfirmed") outgoing.current.unshift(turn);
    else outgoing.current.push(turn);
    drainOutgoing();
  }
  function drainOutgoing() {
    const next = outgoing.current[0];
    if (
      delivering.current ||
      !next ||
      (uncertainDelivery.current && uncertainDelivery.current !== next.id)
    )
      return;
    outgoing.current.shift();
    delivering.current = true;
    void deliver(next);
  }
  async function deliver(turn: VisibleTurn) {
    const wasUnconfirmed = turn.status === "unconfirmed";
    const controller = new AbortController();
    let timedOut = false;
    const acceptanceTimeout = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 15_000);
    setTurns((current) =>
      current.map((t) =>
        t.id === turn.id ? { ...t, status: "sending", error: undefined } : t,
      ),
    );
    try {
      const saved = await api<ChatTurn>("/api/chat/messages", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({ id: turn.id, message: turn.message }),
      });
      confirmed.current.add(turn.id);
      if (uncertainDelivery.current === turn.id)
        uncertainDelivery.current = null;
      setTurns((current) =>
        current.map((t) =>
          t.id === turn.id && ["sending", "unconfirmed"].includes(t.status)
            ? saved
            : t,
        ),
      );
    } catch (err) {
      const rejected =
        !wasUnconfirmed &&
        err instanceof APIError &&
        err.status >= 400 &&
        err.status < 500 &&
        err.status !== 408;
      if (!rejected && !confirmed.current.has(turn.id))
        uncertainDelivery.current = turn.id;
      setTurns((current) =>
        current.map((t) =>
          t.id === turn.id && t.status === "sending"
            ? {
                ...t,
                status: rejected ? "rejected" : "unconfirmed",
                error: timedOut
                  ? "Delivery confirmation timed out. Checking whether your message arrived."
                  : errorText(err),
              }
            : t,
        ),
      );
    } finally {
      clearTimeout(acceptanceTimeout);
      submitLocks.current.delete(turn.id);
      delivering.current = false;
      drainOutgoing();
    }
  }
  function send(e: FormEvent) {
    e.preventDefault();
    const submitted = draftRef.current.trim();
    const attached = assetsRef.current;
    // Sending while a file is still being read would leave it behind.
    if ((!submitted && !attached.length) || readingRef.current) return;
    const composed = composeMessage(submitted, attached);
    const tooLarge = messageLimitError(composed);
    if (tooLarge) {
      setAssetErrors([tooLarge]);
      return;
    }
    const turn: VisibleTurn = {
      id: crypto.randomUUID(),
      message: composed,
      draft: attached.length
        ? { text: submitted, assets: attached }
        : undefined,
      // The daemon has not seen this message yet, so it has no revision of its
      // own; it gets one once the queue accepts it.
      revision: 0,
      created_at: new Date().toISOString(),
      status: "waiting",
      events: [],
    };
    setTurns((current) => [...current, turn]);
    setDraft("");
    assetsRef.current = [];
    setAssets([]);
    setAssetErrors([]);
    void enqueue(turn);
  }
  async function cancel(turn: VisibleTurn) {
    if (turn.status === "waiting") {
      outgoing.current = outgoing.current.filter((t) => t.id !== turn.id);
      submitLocks.current.delete(turn.id);
      setTurns((current) => current.filter((t) => t.id !== turn.id));
      return;
    }
    setCancelling((current) => new Set(current).add(turn.id));
    setError("");
    try {
      const saved = await api<ChatTurn>(
        `/api/chat/messages/${encodeURIComponent(turn.id)}`,
        { method: "DELETE" },
      );
      setTurns((current) => current.map((t) => (t.id === turn.id ? saved : t)));
    } catch (err) {
      setError(errorText(err));
    } finally {
      setCancelling((current) => {
        const next = new Set(current);
        next.delete(turn.id);
        return next;
      });
    }
  }
  return (
    <>
      <header className="chat-header">
        <Avatar avatar={state.assistant.avatar} small />
        <div>
          <h2>{name}</h2>
          <span>Your context, kept together</span>
        </div>
        <button
          className="icon-button chat-expand"
          aria-label={expanded ? "Return to workspace" : "Expand conversation"}
          aria-pressed={expanded}
          onClick={onExpand}
          title={
            expanded ? "Return to workspace" : "Make conversation the main view"
          }
        >
          <Icon name={expanded ? "Shrink" : "Expand"} />
          <span className="chat-expand-label">
            {expanded ? "Return to workspace" : "Expand conversation"}
          </span>
        </button>
        <button
          className="icon-button mobile-close"
          aria-label="Close conversation"
          onClick={onClose}
        >
          <Icon name="Close" />
        </button>
      </header>
      {expanded && (
        <div className="chat-context-strip">
          <span>YOUR WORK, WITH CONTEXT</span>
          <p>
            {state.projects.filter((p) => p.status !== "completed").length}{" "}
            active projects <i>·</i> {pendingDecisions(state.decisions).length}{" "}
            open decisions <i>·</i> one conversation
          </p>
        </div>
      )}
      <div
        className="chat-messages"
        ref={scroll}
        role="log"
        aria-label="Conversation history"
        aria-live="polite"
      >
        {messages.length ? (
          messages.map((m) => (
            <article
              key={m.id}
              className={`message ${m.role === "user" ? "user-message" : "assistant-message"}`}
            >
              <div className="message-label">
                {m.role === "user"
                  ? "You"
                  : m.role === "system"
                    ? "Status"
                    : name}
                {m.created_at && (
                  <time dateTime={m.created_at}>{dateLabel(m.created_at)}</time>
                )}
              </div>
              <ConversationMarkdown
                content={m.content}
                projects={state.projects}
                onProjectOpen={onProjectOpen}
              />
              <TurnStatus
                turn={turnsByMessage.get(m.id)}
                live={
                  m.id === newestMessageID &&
                  !turnReplied(turnsByMessage.get(m.id))
                }
                name={name}
                cancelling={cancelling}
                onCancel={cancel}
                onRestore={(t) => {
                  // Attachments go back to the composer as attachments, not as
                  // the text they were folded into.
                  const text = t.draft ? t.draft.text : t.message;
                  setDraft(
                    draftRef.current && text
                      ? `${draftRef.current}\n\n${text}`
                      : draftRef.current || text,
                  );
                  if (t.draft) {
                    const restored = t.draft.assets;
                    setAssets((current) => [...current, ...restored]);
                  }
                  setTurns((current) => current.filter((x) => x.id !== t.id));
                  document.getElementById("chat-message")?.focus();
                }}
                onDiscard={(t) =>
                  setTurns((current) => current.filter((x) => x.id !== t.id))
                }
                onRetry={enqueue}
              />
            </article>
          ))
        ) : (
          <div className="chat-welcome">
            <span className="chat-orbit">
              <Avatar avatar={state.assistant.avatar} />
            </span>
            <p className="eyebrow">A LITTLE LESS TO CARRY</p>
            <h3>Start a conversation.</h3>
            <p>
              Share an outcome, ask about your projects, or tell {name} what
              matters to you.
            </p>
            <div className="suggestions">
              {[
                "What needs my attention?",
                "Help me set up a project.",
                "What do you remember about me?",
              ].map((text) => (
                <button
                  key={text}
                  onClick={() => {
                    setDraft(text);
                    document.getElementById("chat-message")?.focus();
                  }}
                >
                  {text}
                  <Icon name="Arrow" size={13} />
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
      <div className="chat-composer-wrap">
        {error && (
          <div className="error-notice" role="alert">
            {error}
          </div>
        )}
        {pollError && (
          <p className="chat-update-error" role="status">
            {pollError} Your messages remain saved; reconnecting…
          </p>
        )}
        {turns.some((t) => t.status === "waiting") &&
          turns.some((t) => t.status === "unconfirmed") && (
            <p className="chat-queue-summary">
              Waiting for delivery confirmation before sending the following
              messages. Keep this page open.
            </p>
          )}
        <ChatQueue
          turns={turns.filter((t) => t.status === "queued")}
          revision={queue.revision}
          hold={queue.hold}
          running={!!running}
          cancelling={cancelling}
          onCancel={(id) => {
            const turn = turns.find((t) => t.id === id);
            if (turn) void cancel(turn);
          }}
          onChanged={() => refreshRef.current()}
        />
        {assetErrors.length > 0 && (
          <div className="error-notice composer-asset-errors" role="alert">
            {assetErrors.map((text) => (
              <p key={text}>{text}</p>
            ))}
          </div>
        )}
        <form
          className={`chat-composer${dragging ? " composer-dragging" : ""}`}
          onSubmit={send}
          onDragOver={(e) => {
            if (!carriesFiles(e.dataTransfer)) return;
            e.preventDefault();
            setDragging(true);
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node | null))
              setDragging(false);
          }}
          onDrop={(e) => {
            setDragging(false);
            const files = Array.from(e.dataTransfer?.files || []);
            // Dropped text falls through to the textarea as usual.
            if (!files.length) return;
            e.preventDefault();
            void addFiles(files);
          }}
        >
          <label className="sr-only" htmlFor="chat-message">
            Message {name}
          </label>
          {(assets.length > 0 || reading > 0) && (
            <ul className="composer-assets" aria-label="Attachments">
              {assets.map((asset) => (
                <li key={asset.id}>
                  <span className="composer-asset-name">{asset.name}</span>
                  <span className="composer-asset-size">
                    {sizeLabel(asset.size)}
                  </span>
                  <button
                    type="button"
                    className="icon-button"
                    aria-label={`Remove attachment ${asset.name}`}
                    onClick={() =>
                      setAssets((current) =>
                        current.filter((a) => a.id !== asset.id),
                      )
                    }
                  >
                    <Icon name="Close" size={12} />
                  </button>
                </li>
              ))}
              {reading > 0 && (
                <li className="composer-asset-reading">Reading files…</li>
              )}
            </ul>
          )}
          <textarea
            id="chat-message"
            value={message}
            onChange={(e) => setDraft(e.target.value)}
            onPaste={(e) => {
              // Anything with text pastes as text; a clipboard of only files
              // (a screenshot, a copied file) becomes attachments.
              if (e.clipboardData.getData("text/plain")) return;
              const files = Array.from(e.clipboardData.files || []);
              if (!files.length) return;
              e.preventDefault();
              void addFiles(files);
            }}
            placeholder={`Ask ${name}, or hand over an outcome…`}
            rows={3}
            maxLength={20000}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing &&
                e.keyCode !== 229
              ) {
                e.preventDefault();
                e.currentTarget.form?.requestSubmit();
              }
            }}
          />
          <div className="composer-footer">
            <span>
              Enter to send · Shift + Enter for a new line · Drop or paste text
              files to attach
            </span>
            <button
              className="send-button"
              type="submit"
              disabled={(!message.trim() && !assets.length) || reading > 0}
              aria-label="Send message"
            >
              <Icon name="Send" size={17} />
            </button>
          </div>
        </form>
        <p className="chat-footnote">One conversation across your projects.</p>
      </div>
    </>
  );
}
