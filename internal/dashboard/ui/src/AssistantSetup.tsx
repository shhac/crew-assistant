import { useEffect, useRef, useState, type FormEvent } from "react";
import { Panel } from "./SettingsPanel";
import { Avatar, ErrorNotice, hasFace } from "./ui";
import { api, errorText, type AvatarSpec, type Config } from "./api";
interface Recommendation {
  id: string;
  name: string;
  personality: string;
  avatar: AvatarSpec;
  rationale: string;
  avatar_svg?: string;
  applied?: boolean;
}
interface SetupState {
  messages: { role: string; content: string }[];
  recommendation?: Recommendation;
  questions: string[];
}
export function AssistantSetup({
  currentName,
  onApplied,
  demo,
}: {
  currentName: string;
  onApplied: (assistant: NonNullable<Config["assistant"]>) => Promise<void>;
  demo: boolean;
}) {
  const [state, setState] = useState<SetupState>({
    messages: [],
    questions: [],
  });
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState("");
  const [applied, setApplied] = useState(false);
  const log = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let active = true;
    api<SetupState>("/api/setup")
      .then((value) => {
        if (active)
          setState({
            ...value,
            messages: value.messages || [],
            questions: value.questions || [],
          });
      })
      .catch((e) => {
        if (active) setError(errorText(e));
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [state.messages.length, busy]);
  async function interview(e?: FormEvent) {
    e?.preventDefault();
    if (busy || demo) return;
    setBusy(true);
    setError("");
    setApplied(false);
    try {
      const next = await api<SetupState>("/api/setup/interview", {
        method: "POST",
        body: JSON.stringify({ message: message.trim() }),
      });
      setState({
        ...next,
        messages: next.messages || [],
        questions: next.questions || [],
      });
      setMessage("");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function apply() {
    if (!state.recommendation) return;
    setApplying(true);
    setError("");
    try {
      const assistant = await api<NonNullable<Config["assistant"]>>(
        "/api/setup/apply",
        {
          method: "POST",
          body: JSON.stringify({
            recommendation_id: state.recommendation.id,
            accepted: true,
          }),
        },
      );
      await onApplied(assistant);
      setApplied(true);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setApplying(false);
    }
  }
  const proposal = state.recommendation;
  const isApplied = applied || proposal?.applied === true;
  return (
    <Panel title="Suggest a name and personality" id="setup-title">
      <p className="soft">
        Answer a question or two, and the assistant suggests a name, a
        personality and an avatar.
      </p>
      <ErrorNotice error={error} />
      {demo && <p className="muted small">Not available in the demo.</p>}
      {state.messages.length > 0 && (
        <div
          className="setup-log"
          role="log"
          aria-label="Suggestion conversation"
          ref={log}
        >
          {state.messages.map((m, i) => (
            <p
              key={i}
              className={m.role === "user" ? "setup-you" : "setup-them"}
            >
              <span className="label">
                {m.role === "user" ? "You" : currentName || "Assistant"}
              </span>
              {m.content}
            </p>
          ))}
        </div>
      )}
      {!state.messages.length && !busy && (
        <div className="actions">
          <button
            type="button"
            className="btn"
            disabled={demo}
            onClick={() => void interview()}
          >
            Start
          </button>
        </div>
      )}
      {busy && (
        <p className="muted small" role="status">
          Working on a suggestion…
        </p>
      )}
      {state.messages.length > 0 && (
        <div className="form">
          <label htmlFor="setup-answer">
            Your answer
            <textarea
              id="setup-answer"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={2}
              maxLength={12000}
              placeholder="Short updates, a calm tone…"
              disabled={busy || demo}
            />
          </label>
          <div className="actions">
            <button
              type="button"
              className="btn"
              disabled={busy || demo || !message.trim()}
              onClick={() => void interview()}
            >
              Send
            </button>
          </div>
        </div>
      )}
      {proposal && (
        <div className="setup-proposal">
          <div className="setup-proposal-head">
            <Avatar of={proposal} size={96} />
            <div className="setup-proposal-name">
              <h3>{proposal.name}</h3>
              {hasFace(proposal) && (
                <p className="setup-favicon muted small">
                  <Avatar of={proposal} size={16} />
                  As the tab icon
                </p>
              )}
            </div>
          </div>
          <p>{proposal.personality}</p>
          <p className="muted small">{proposal.rationale}</p>
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={applying || busy || isApplied || demo}
              onClick={() => void apply()}
            >
              {isApplied ? "In use" : "Use this"}
            </button>
            <span className="muted small" role="status">
              {isApplied ? "Saved." : "Nothing changes until you use it."}
            </span>
          </div>
        </div>
      )}
    </Panel>
  );
}
