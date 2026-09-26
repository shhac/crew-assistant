import { useEffect, useRef, useState, type FormEvent } from "react";
import { Panel } from "./SettingsPanel";
import { Avatar, hasFace } from "./Avatar";
import { ErrorNotice } from "./ui";
import { api, errorText, type AvatarSpec, type Face } from "./api";

/** Who a suggestion is for. */
export type SetupSubject = "assistant" | "member";

/** A suggested name, personality and look, to fill in a form with. */
export interface Suggestion extends Face {
  id: string;
  name: string;
  personality: string;
  avatar: AvatarSpec;
  rationale: string;
}
interface SetupState {
  messages: { role: string; content: string }[];
  recommendation?: Suggestion;
  questions: string[];
}

const setupPath = (subject: SetupSubject) => `/api/setup/${subject}`;

/** Starts a subject's suggestions afresh, as once who it suggested is added. */
export function resetSuggestion(subject: SetupSubject) {
  return api(setupPath(subject), { method: "DELETE" });
}

const intro: Record<SetupSubject, string> = {
  assistant:
    "Answer a question or two, and your assistant suggests a name, a personality and a look for the new assistant.",
  member:
    "Answer a question or two, and your assistant suggests a name, a personality and a look for the new member.",
};

/**
 * "Suggest a name and personality" for someone new on the team. Using a
 * suggestion fills in the form beside it; nothing is saved until the owner
 * adds them.
 */
export function SuggestIdentity({
  subject,
  askerName,
  demo,
  onUse,
}: {
  subject: SetupSubject;
  /** Who makes the suggestion: the assistant in the seat. */
  askerName: string;
  demo: boolean;
  onUse: (suggestion: Suggestion) => void;
}) {
  const [state, setState] = useState<SetupState>({
    messages: [],
    questions: [],
  });
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [used, setUsed] = useState("");
  const log = useRef<HTMLDivElement>(null);
  useEffect(() => {
    let active = true;
    api<SetupState>(setupPath(subject))
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
  }, [subject]);
  useEffect(() => {
    if (log.current) log.current.scrollTop = log.current.scrollHeight;
  }, [state.messages.length, busy]);
  async function interview(e?: FormEvent) {
    e?.preventDefault();
    if (busy || demo) return;
    setBusy(true);
    setError("");
    try {
      const next = await api<SetupState>(`${setupPath(subject)}/interview`, {
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
  async function startOver() {
    setError("");
    try {
      await resetSuggestion(subject);
      setState({ messages: [], questions: [] });
      setUsed("");
    } catch (e) {
      setError(errorText(e));
    }
  }
  const proposal = state.recommendation;
  const isUsed = !!proposal && used === proposal.id;
  return (
    <Panel title="Suggest a name and personality" id={`suggest-${subject}`}>
      <p className="soft">{intro[subject]}</p>
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
                {m.role === "user" ? "You" : askerName || "Assistant"}
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
          <label htmlFor={`setup-answer-${subject}`}>
            Your answer
            <textarea
              id={`setup-answer-${subject}`}
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
            <button
              type="button"
              className="btn btn-quiet"
              disabled={busy}
              onClick={() => void startOver()}
            >
              Start over
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
              {subject === "assistant" && hasFace(proposal) && (
                <p className="setup-favicon muted small">
                  <Avatar of={proposal} size={16} />
                  As the tab icon
                </p>
              )}
            </div>
          </div>
          {proposal.avatar.look && (
            <p className="soft small">
              How they'll look: {proposal.avatar.look}
            </p>
          )}
          <p className="muted small">Codex draws this once they're added.</p>
          <p>{proposal.personality}</p>
          <p className="muted small">{proposal.rationale}</p>
          <div className="actions">
            <button
              type="button"
              className="btn btn-primary"
              disabled={busy || demo}
              onClick={() => {
                onUse(proposal);
                setUsed(proposal.id);
              }}
            >
              Use this
            </button>
            <span className="muted small" role="status">
              {isUsed
                ? "Filled in. Nothing is saved until you add them."
                : "Nothing changes until you use it."}
            </span>
          </div>
        </div>
      )}
    </Panel>
  );
}
