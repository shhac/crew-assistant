import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  api,
  APIError,
  errorText,
  enginePaused,
  type ChatSession,
  type ChatTurn,
  type State,
} from "./api";
import { ConversationMarkdown } from "./ConversationMarkdown";
import { engineLabel } from "./engines";
import { Avatar } from "./Avatar";
import { dateLabel, Icon } from "./ui";
import { ChatQueue, type QueueHold } from "./ChatQueue";
import { ChatHistory } from "./ChatHistory";
import {
  composeMessage,
  messageLimitError,
  readAsset,
  useFileDrop,
} from "./composerAssets";
import {
  active,
  chronological,
  commandIn,
  commandLabel,
  latestTurn,
  turnLive,
  unsettled,
  wakeSummary,
  type VisibleTurn,
} from "./chatTurns";
import { SessionLine } from "./ChatSessionLine";
import { Composer, LargeEditor, focusDraft } from "./Composer";
import { TurnStatus } from "./TurnStatus";
import type { ChatDraft } from "./chatDraft";

// How long the conversation must stay settled before a next message is
// suggested, so a reply that has only just landed is not raced.
export const SUGGESTION_DELAY = 1200;
export function ChatPanel({
  draft,
  state,
  refresh,
  onClose,
  expanded,
  onExpand,
  onProjectOpen,
  view,
}: {
  draft: ChatDraft;
  state: State;
  refresh: () => Promise<void>;
  onClose: () => void;
  expanded: boolean;
  onExpand: () => void;
  onProjectOpen?: (id: string) => void;
  /** Where the owner is in the dashboard; a change discards any suggestion. */
  view?: string;
}) {
  const [large, setLarge] = useState(false);
  const priorLarge = useRef(false);
  useEffect(() => {
    if (priorLarge.current && !large) focusDraft();
    priorLarge.current = large;
  }, [large]);
  const { message, setMessage, assets, setAssets } = draft;
  const draftRef = useRef(message);
  draftRef.current = message;
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
  const assetsRef = useRef(assets);
  assetsRef.current = assets;
  const [assetErrors, setAssetErrors] = useState<string[]>([]);
  const [reading, setReading] = useState(0);
  const readingRef = useRef(0);
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
  const { dragging, dropHandlers, onPaste } = useFileDrop(
    (files) => void addFiles(files),
  );
  const [turns, setTurns] = useState<VisibleTurn[]>([]);
  const [error, setError] = useState("");
  const [pollError, setPollError] = useState("");
  const [queue, setQueue] = useState<{
    hold?: QueueHold | null;
    revision: number;
  }>({ revision: 0 });
  const [session, setSession] = useState<ChatSession | null>(null);
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [history, setHistory] = useState(false);
  // Asks the daemon for its turns at once, as after picking up a past
  // conversation.
  const pollNow = useRef<() => void>(() => {});
  // When each message was accepted, counted, so a poll can tell a turn it
  // should have listed from one accepted after it asked.
  const acceptances = useRef(0);
  const accepted = useRef(new Map<string, number>());
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
        command: t.command || commandIn(t.message),
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
    let polling = false;
    let again = false;
    async function poll() {
      // One request at a time: an answer that overtook a newer one could
      // bring back turns the newer one had already left behind.
      if (polling) {
        again = true;
        return;
      }
      polling = true;
      clearTimeout(timer);
      try {
        const asked = acceptances.current;
        const result = await api<{
          turns: ChatTurn[];
          hold: QueueHold | null;
          revision: number;
          conversation?: string;
          session?: ChatSession;
        }>("/api/chat/turns");
        if (stopped) return;
        setQueue({ hold: result.hold, revision: result.revision });
        setSession(result.session ?? null);
        const incoming = result.turns || [];
        // The daemon lists every turn of the current conversation and every
        // turn still waiting. A turn it already knew of when asked, and left
        // out, belongs to a conversation that has been put away, so it goes.
        // Only one accepted since the question was asked may be missing
        // because the answer is older than it.
        const listed = new Set(incoming.map((t) => t.id));
        const gone = new Set(
          [...confirmed.current].filter(
            (id) => !listed.has(id) && (accepted.current.get(id) ?? 0) <= asked,
          ),
        );
        incoming.forEach((t) => confirmed.current.add(t.id));
        if (
          uncertainDelivery.current &&
          confirmed.current.has(uncertainDelivery.current)
        ) {
          uncertainDelivery.current = null;
          drainOutgoing();
        }
        const signature =
          `${result.conversation}|` +
          incoming
            .map(
              (t) =>
                `${t.id}:${t.status}:${t.user_message_id}:${t.assistant_message_id}`,
            )
            .join("|");
        setTurns((current) => {
          const merged = new Map(
            current.filter((t) => !gone.has(t.id)).map((t) => [t.id, t]),
          );
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
        polling = false;
        if (again && !stopped) {
          again = false;
          void poll();
        } else if (!stopped)
          timer = setTimeout(poll, turnsRef.current.some(active) ? 1000 : 3000);
      }
    }
    pollNow.current = () => void poll();
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
      accepted.current.set(turn.id, ++acceptances.current);
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
    // A command is the whole message. Nothing is sent, and the attachments
    // stay, rather than the command going to the model with them.
    if (commandIn(submitted) && attached.length) {
      setAssetErrors([
        `Send ${submitted} on its own. Your attachments stay here for your next message.`,
      ]);
      return;
    }
    const composed = composeMessage(submitted, attached);
    const tooLarge = messageLimitError(composed);
    if (tooLarge) {
      setAssetErrors([tooLarge]);
      return;
    }
    submit(
      composed,
      attached.length ? { text: submitted, assets: attached } : undefined,
    );
    setDraft("");
    setLarge(false);
    assetsRef.current = [];
    setAssets([]);
    setAssetErrors([]);
  }
  function submit(composed: string, draft?: VisibleTurn["draft"]) {
    const turn: VisibleTurn = {
      id: crypto.randomUUID(),
      message: composed,
      draft,
      // The daemon has not seen this message yet, so it has no revision of its
      // own; it gets one once the queue accepts it.
      revision: 0,
      created_at: new Date().toISOString(),
      status: "waiting",
      events: [],
    };
    setTurns((current) => [...current, turn]);
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
  const composerProps = {
    id: "chat-message",
    value: message,
    onChange: setDraft,
    assets,
    onRemove: (id: string) =>
      setAssets((current) => current.filter((a) => a.id !== id)),
    reading,
    suggestion: shownSuggestion,
    onSubmit: send,
    name,
    showHint: messages.length === 0,
    onExpand: () => setLarge(true),
    dragging,
    dropHandlers,
    onPaste,
  };
  return (
    <div className="chat">
      <header className="chat-head">
        <Avatar of={state.assistant} size={20} />
        <h2>{name}</h2>
        <button
          className="btn btn-quiet btn-icon"
          aria-label="Past conversations"
          aria-pressed={history}
          title="Past conversations"
          onClick={() => setHistory((shown) => !shown)}
        >
          <Icon name="Clock" />
        </button>
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
      {session && !history && (
        <SessionLine
          session={session}
          busy={turns.some(unsettled)}
          onCommand={(command) => submit(`/${command}`)}
        />
      )}
      {history && (
        <ChatHistory
          name={name}
          projects={state.projects}
          onProjectOpen={onProjectOpen}
          onResumed={async () => {
            await refreshRef.current();
            pollNow.current();
          }}
          onClose={() => setHistory(false)}
        />
      )}
      <div
        hidden={history}
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
            const command = "command" in m ? m.command : undefined;
            if (command)
              return (
                <div key={m.id} className="wake chat-command">
                  <p className="wake-head">
                    <span className="pill">/{command}</span>
                    <span className="wake-line">
                      {turn?.outcome || commandLabel(command)}
                      {turn?.origin === "assistant" && ` · asked by ${name}`}
                    </span>
                  </p>
                  {status}
                </div>
              );
            if (m.role === "summary")
              return (
                <details key={m.id} className="chat-summary" open>
                  <summary>Summary of the conversation so far</summary>
                  <div className="message-body">
                    <ConversationMarkdown
                      content={m.content}
                      projects={state.projects}
                      onProjectOpen={onProjectOpen}
                    />
                  </div>
                </details>
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
                  {m.role !== "user" && (
                    <Avatar of={state.assistant} size={16} />
                  )}
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
      {/* While a past conversation is open, there is nothing to write in:
          a message or command would go to the current conversation, not the
          one on screen. The draft and attachments wait for the return. */}
      {!history && (
        <div className="chat-foot">
          {state.assistant.engine &&
            enginePaused(state.engine_pauses, state.assistant.engine) && (
              <p className="muted small">
                {engineLabel(state.assistant.engine)} is paused. Your messages
                still send.
              </p>
            )}
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
                The next messages wait until this one is confirmed. Keep this
                page open.
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
          {!large && assetErrors.length > 0 && (
            <div className="error" role="alert">
              {assetErrors.map((text, i) => (
                <p key={`${i}:${text}`}>{text}</p>
              ))}
            </div>
          )}
          {!large && <Composer {...composerProps} />}
          {large && (
            <LargeEditor onDone={() => setLarge(false)}>
              {assetErrors.length > 0 && (
                <div className="error" role="alert">
                  {assetErrors.map((text, i) => (
                    <p key={i}>{text}</p>
                  ))}
                </div>
              )}
              <Composer {...composerProps} large />
            </LargeEditor>
          )}
          {suggestionsOff && (
            <p className="muted small" role="status">
              {suggestionsOff}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
