import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError, errorText, type ChatTurn, type State } from "./api";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { dateLabel, fullDateLabel, Icon } from "./ui";
import { ChatQueue, type QueueHold } from "./ChatQueue";
import { ToolActivity } from "./ToolActivity";
import {
  carriesFiles,
  composeMessage,
  messageLimitError,
  readAsset,
  sizeLabel,
  type ComposerAsset,
} from "./composerAssets";
type VisibleTurn = Omit<ChatTurn, "status"> & {
  status:
    ChatTurn["status"] | "waiting" | "sending" | "unconfirmed" | "rejected";
  /** What the composer held, so a refused message restores its attachments. */
  draft?: { text: string; assets: ComposerAsset[] };
};
// A turn has spoken once its reply is in the thread; until then its newest
// tool step is still the most recent thing the owner has to look at.
const turnLive = (turn?: VisibleTurn) =>
  turn?.status === "queued" || turn?.status === "running";
const active = (turn: VisibleTurn) =>
  ["waiting", "sending", "queued", "running"].includes(turn.status);
// A turn in any of these states means the conversation is not waiting on the
// owner: a message is on its way, being answered, or needs their recovery.
const unsettled = (turn: VisibleTurn) =>
  active(turn) || turn.status === "unconfirmed" || turn.status === "rejected";
// How long the conversation must stay settled before a next message is
// suggested, so a reply that has only just landed is not raced.
export const SUGGESTION_DELAY = 1200;
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

/** A message's delivery, in a few words; nothing once all is well. */
function delivery(turn: VisibleTurn) {
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
  const said = delivery(turn);
  const retry = fullDateLabel(turn.retry_at);
  return (
    <div className="turn-status">
      {said && (
        <p className="turn-delivery">
          {said}
          {turn.status === "waiting" && (
            <button
              type="button"
              className="link-button"
              disabled={cancelling.has(turn.id)}
              onClick={() => void onCancel(turn)}
              aria-label={`Cancel message: ${turn.message}`}
            >
              Cancel
            </button>
          )}
        </p>
      )}
      {turn.error && (
        <p className="error" role="alert">
          {turn.error}
        </p>
      )}
      {turn.status === "unconfirmed" && (
        <div className="turn-recovery">
          <p className="muted small">
            Retrying is safe: it can't start a second reply.
          </p>
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => void onRetry(turn)}
          >
            Retry
          </button>
        </div>
      )}
      {turn.status === "rejected" && (
        <div className="turn-recovery actions">
          <button
            type="button"
            className="btn btn-sm"
            onClick={() => onRestore(turn)}
          >
            Edit
          </button>
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => onDiscard(turn)}
          >
            Discard
          </button>
        </div>
      )}
      {!!turn.events?.length && (
        <ToolActivity events={turn.events} live={live} />
      )}
      {turn.status === "running" && (
        <p className="turn-working" role="status">
          <span className="dot" aria-hidden="true" />
          {turn.model_status || turn.loading_phrase || `${name} is working`}
          {retry && <span className="muted small"> · retrying at {retry}</span>}
        </p>
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
  view,
}: {
  state: State;
  refresh: () => Promise<void>;
  onClose: () => void;
  expanded: boolean;
  onExpand: () => void;
  onProjectOpen?: (id: string) => void;
  /** Where the owner is in the dashboard; a change discards any suggestion. */
  view?: string;
}) {
  const [message, setMessage] = useState("");
  const draftRef = useRef("");
  const [suggestion, setSuggestion] = useState<{
    after: string;
    text: string;
  } | null>(null);
  const [suggestionsOff, setSuggestionsOff] = useState("");
  // Replies a suggestion was already asked for, dismissed or discarded. Each
  // reply gets at most one, so a dismissed suggestion does not come back.
  const suggested = useRef(new Set<string>());
  function setDraft(value: string) {
    draftRef.current = value;
    setMessage(value);
    // The owner's own words always replace a suggestion.
    if (value) setSuggestion(null);
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
    // Attaching is composing, so it dismisses a suggestion as typing does.
    setSuggestion(null);
    // Old refusals clear only when no other files are still being read;
    // a slow read finishing later must not erase a refusal that came after it.
    if (!readingRef.current) setAssetErrors([]);
    readingRef.current += files.length;
    setReading(readingRef.current);
    const results = await Promise.all(files.map(readAsset));
    readingRef.current -= files.length;
    setReading(readingRef.current);
    const added = results.flatMap((r) => ("asset" in r ? [r.asset] : []));
    const refused = results.flatMap((r) => ("error" in r ? [r.error] : []));
    setAssets((current) => [...current, ...added]);
    setAssetErrors((current) => [...current, ...refused]);
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
  // The last rendered message in the thread. While it has no reply, its
  // steps are summed up by what it is doing now rather than how it went.
  const newestMessageID = messages[messages.length - 1]?.id;
  const turnsByMessage = new Map(
    turns.map((t) => [t.user_message_id || t.id, t]),
  );
  // Settled: the newest message is the assistant's reply and nothing is being
  // sent, queued or answered. Only then is a next message suggested, and only
  // while the draft is empty. Pending or loading attachments are a draft too.
  const newest = messages[messages.length - 1];
  const settledOn =
    newest?.role === "assistant" && !turns.some(unsettled) ? newest.id : "";
  const draftEmpty = !message && !assets.length && !reading;
  const shownSuggestion =
    suggestion && suggestion.after === settledOn && draftEmpty
      ? suggestion.text
      : "";
  const shownView = useRef(view);
  // Declared before the request effect so its cleanup has aborted any request
  // before this marks the reply as done.
  useEffect(() => {
    if (shownView.current === view) return;
    shownView.current = view;
    if (settledOn) suggested.current.add(settledOn);
    setSuggestion(null);
  }, [view]);
  useEffect(() => {
    if (
      !settledOn ||
      !draftEmpty ||
      suggestionsOff ||
      suggested.current.has(settledOn)
    )
      return;
    const after = settledOn;
    const controller = new AbortController();
    const timer = setTimeout(() => {
      suggested.current.add(after);
      api<{ after: string; suggestion: string }>("/api/chat/suggestion", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({ after }),
      })
        .then((reply) => {
          // A reply for a conversation that has since moved on, or that
          // arrives after the owner started typing, is dropped.
          if (
            !controller.signal.aborted &&
            reply.after === after &&
            reply.suggestion &&
            !draftRef.current &&
            !assetsRef.current.length &&
            !readingRef.current
          )
            setSuggestion({ after, text: reply.suggestion });
        })
        .catch((err) => {
          // A failed suggestion leaves the composer as it was. Only a model
          // that cannot be used as approved is worth telling the owner about.
          if (
            !controller.signal.aborted &&
            err instanceof APIError &&
            err.status === 503
          )
            setSuggestionsOff(errorText(err));
        });
    }, SUGGESTION_DELAY);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [settledOn, draftEmpty, suggestionsOff, view]);
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
          setPollError(`Can't get live updates (${errorText(err)}).`);
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
                  ? "Checking whether it arrived."
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
    <div className="chat">
      <header className="chat-head">
        <h2>{name}</h2>
        <button
          className="btn btn-quiet btn-icon chat-expand"
          aria-label={expanded ? "Back to the work" : "Widen the chat"}
          aria-pressed={expanded}
          title={expanded ? "Back to the work" : "Widen the chat"}
          onClick={onExpand}
        >
          <Icon name={expanded ? "Shrink" : "Expand"} />
        </button>
        <button
          className="btn btn-quiet btn-icon mobile-close"
          aria-label="Close chat"
          title="Close chat (⌘J)"
          onClick={onClose}
        >
          <Icon name="Close" />
        </button>
      </header>
      <div
        className="chat-log"
        ref={scroll}
        role="log"
        aria-label="Conversation"
        aria-live="polite"
      >
        {messages.length ? (
          messages.map((m) => {
            const wake = "origin" in m && m.origin === "wake";
            const turn = turnsByMessage.get(m.id);
            const status = (
              <TurnStatus
                turn={turn}
                live={m.id === newestMessageID && turnLive(turn)}
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
            );
            if (wake)
              return (
                <div key={m.id} className="wake">
                  <p className="wake-head">
                    <span className="pill">Wake-up</span>
                    <span className="wake-line">{wakeSummary(m.content)}</span>
                    {m.created_at && (
                      <time className="muted small" dateTime={m.created_at}>
                        {dateLabel(m.created_at)}
                      </time>
                    )}
                  </p>
                  {status}
                </div>
              );
            return (
              <article
                key={m.id}
                className={`message ${m.role === "user" ? "from-you" : "from-assistant"}`}
              >
                <p className="message-by">
                  <span>{m.role === "user" ? "You" : name}</span>
                  {m.created_at && (
                    <time dateTime={m.created_at}>
                      {dateLabel(m.created_at)}
                    </time>
                  )}
                </p>
                <div className="message-body">
                  <ConversationMarkdown
                    content={m.content}
                    projects={state.projects}
                    onProjectOpen={onProjectOpen}
                  />
                </div>
                {status}
              </article>
            );
          })
        ) : (
          <div className="chat-welcome">
            <p className="soft">
              Ask about your projects, or hand {name} something to do.
            </p>
            <div className="chat-starters">
              {[
                "What needs me today?",
                "Start a new project",
                "What do you remember about me?",
              ].map((text) => (
                <button
                  key={text}
                  type="button"
                  className="btn btn-sm"
                  onClick={() => {
                    setDraft(text);
                    document.getElementById("chat-message")?.focus();
                  }}
                >
                  {text}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
      <div className="chat-foot">
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        {pollError && (
          <p className="muted small" role="status">
            {pollError} Trying again…
          </p>
        )}
        {turns.some((t) => t.status === "waiting") &&
          turns.some((t) => t.status === "unconfirmed") && (
            <p className="queue-hint">
              The next messages wait until this one is confirmed. Keep this page
              open.
            </p>
          )}
        <ChatQueue
          turns={turns.filter((t) => t.status === "queued")}
          revision={queue.revision}
          hold={queue.hold}
          cancelling={cancelling}
          onCancel={(id) => {
            const turn = turns.find((t) => t.id === id);
            if (turn) void cancel(turn);
          }}
          onChanged={() => refreshRef.current()}
        />
        {assetErrors.length > 0 && (
          <div className="error" role="alert">
            {assetErrors.map((text, i) => (
              <p key={`${i}:${text}`}>{text}</p>
            ))}
          </div>
        )}
        <form
          className={`composer${dragging ? " dragging" : ""}`}
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
            <ul className="attachments" aria-label="Attachments">
              {assets.map((asset) => (
                <li key={asset.id}>
                  <span className="attachment-name">{asset.name}</span>
                  <span className="muted small">{sizeLabel(asset.size)}</span>
                  <button
                    type="button"
                    className="btn btn-quiet btn-icon btn-sm"
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
              {reading > 0 && <li className="muted small">Reading files…</li>}
            </ul>
          )}
          <div className="composer-row">
            <textarea
              id="chat-message"
              value={message}
              onChange={(e) => setDraft(e.target.value)}
              onPaste={(e) => {
                // Text always pastes as text. Files on the same clipboard (a
                // copied file comes with its name as text) are still attached
                // or refused with a reason, never dropped.
                const files = Array.from(e.clipboardData.files || []);
                if (!files.length) return;
                if (!e.clipboardData.getData("text/plain")) e.preventDefault();
                void addFiles(files);
              }}
              // A suggestion is shown, never committed: the draft stays empty
              // and Send stays disabled until the owner takes it.
              placeholder={shownSuggestion || `Message ${name}`}
              className={shownSuggestion ? "has-suggestion" : undefined}
              rows={2}
              maxLength={20000}
              onKeyDown={(e) => {
                if (
                  e.key === "Tab" &&
                  shownSuggestion &&
                  !e.shiftKey &&
                  !e.altKey &&
                  !e.ctrlKey &&
                  !e.metaKey
                ) {
                  // Accepting makes it an ordinary draft to edit; it is not sent.
                  e.preventDefault();
                  setDraft(shownSuggestion);
                  return;
                }
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
            <button
              className="btn btn-primary btn-icon"
              type="submit"
              disabled={(!message.trim() && !assets.length) || reading > 0}
              aria-label="Send"
            >
              <Icon name="Send" size={15} />
            </button>
          </div>
          <p className="composer-hint">
            {shownSuggestion ? (
              <>
                <span className="kbd">Tab</span> takes the suggestion
              </>
            ) : messages.length > 0 ? null : (
              <>
                <span className="kbd">Enter</span> sends ·{" "}
                <span className="kbd">Shift Enter</span> new line · drop text
                files to attach
              </>
            )}
          </p>
        </form>
        {suggestionsOff && (
          <p className="muted small" role="status">
            {suggestionsOff}
          </p>
        )}
      </div>
    </div>
  );
}
