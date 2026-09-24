import { useState, type FormEvent } from "react";
import { dateLabel, ErrorNotice, recordedTime } from "./ui";
import { api, errorText, type Memory, type State } from "./api";

const groups = [
  {
    kind: "preference",
    title: "Your preferences",
    note: "",
  },
  {
    kind: "observation",
    title: "Observations",
    note: "Can go out of date",
  },
  { kind: "", title: "Other notes", note: "" },
];

const kindOf = (m: Memory) =>
  m.kind === "preference" || m.kind === "observation" ? m.kind : "";

export function MemoryView({
  state,
  refresh,
}: {
  state: State;
  refresh: () => Promise<void>;
}) {
  const [content, setContent] = useState("");
  const [kind, setKind] = useState("preference");
  const [busy, setBusy] = useState(false);
  // Keyed per memory: acting on one must not disable every other.
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<string | null>(null);
  const [correcting, setCorrecting] = useState<string | null>(null);
  const [correction, setCorrection] = useState("");
  async function add(e: FormEvent) {
    e.preventDefault();
    if (!content.trim()) return;
    setBusy(true);
    setError("");
    try {
      await api("/api/memories", {
        method: "POST",
        body: JSON.stringify({ content: content.trim(), kind }),
      });
      setContent("");
      await refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  async function correct(id: string) {
    if (!correction.trim()) return;
    setPending(id);
    setError("");
    try {
      await api(`/api/memories/${encodeURIComponent(id)}/correct`, {
        method: "POST",
        body: JSON.stringify({ content: correction.trim() }),
      });
      setCorrecting(null);
      setCorrection("");
      await refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setPending(null);
    }
  }
  async function remove(id: string) {
    setPending(id);
    setError("");
    try {
      await api(`/api/memories/${encodeURIComponent(id)}`, {
        method: "DELETE",
      });
      setConfirm(null);
      await refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setPending(null);
    }
  }
  // An unset time arrives as the year one, so presence alone means nothing.
  const corrected = (m: Memory) => !!recordedTime(m.superseded_at);
  const current = state.memories.filter((m) => !corrected(m));
  const earlier = state.memories.filter(corrected);
  return (
    <div className="page memory">
      <header className="page-header">
        <h1>Memory</h1>
        <p className="muted">What the assistant remembers between chats.</p>
      </header>
      <form className="memory-add card" onSubmit={add}>
        <label className="sr-only" htmlFor="memory">
          Something to remember
        </label>
        <input
          id="memory"
          className="field"
          value={content}
          onChange={(e) => setContent(e.target.value)}
          placeholder="Remember something, such as “keep updates to three lines”"
          maxLength={10000}
          required
        />
        <label className="sr-only" htmlFor="memory-kind">
          Kind
        </label>
        <select
          id="memory-kind"
          className="field memory-kind"
          value={kind}
          onChange={(e) => setKind(e.target.value)}
        >
          <option value="preference">Preference</option>
          <option value="observation">Observation</option>
        </select>
        <button className="btn btn-primary" disabled={busy || !content.trim()}>
          Remember
        </button>
      </form>
      <ErrorNotice error={error} />
      {!state.memories.length && (
        <p className="muted">
          Nothing remembered yet. Add something above, or tell the assistant.
        </p>
      )}
      {groups.map((group) => {
        const members = current.filter((m) => kindOf(m) === group.kind);
        if (!members.length) return null;
        return (
          <section
            key={group.title}
            className="section"
            aria-label={group.title}
          >
            <div className="section-title">
              <h2>{group.title}</h2>
              <span className="count">{members.length}</span>
              {group.note && <span className="muted small">{group.note}</span>}
            </div>
            <ul className="card rows">
              {members.map((m) => (
                <li key={m.id} className="memory-row">
                  {correcting === m.id ? (
                    <div className="form memory-correct">
                      <label htmlFor={`correct-${m.id}`}>
                        What should it say?
                        <textarea
                          id={`correct-${m.id}`}
                          className="field"
                          rows={2}
                          maxLength={10000}
                          value={correction}
                          onChange={(e) => setCorrection(e.target.value)}
                        />
                        <span className="hint">
                          The original is kept, marked as corrected.
                        </span>
                      </label>
                      <div className="actions">
                        <button
                          type="button"
                          className="btn btn-primary btn-sm"
                          disabled={pending === m.id || !correction.trim()}
                          onClick={() => void correct(m.id)}
                        >
                          Save
                        </button>
                        <button
                          type="button"
                          className="btn btn-quiet btn-sm"
                          onClick={() => {
                            setCorrecting(null);
                            setCorrection("");
                          }}
                        >
                          Cancel
                        </button>
                      </div>
                    </div>
                  ) : (
                    <>
                      <div className="memory-text">
                        <p>{m.content}</p>
                        <p className="muted small">
                          {m.source === "owner"
                            ? "From you"
                            : m.source === "assistant"
                              ? "From the assistant"
                              : "Source unknown"}
                          {dateLabel(m.updated_at) &&
                            ` · ${dateLabel(m.updated_at)}`}
                          {m.supersedes && " · corrected"}
                        </p>
                      </div>
                      {confirm === m.id ? (
                        <div className="actions">
                          <button
                            type="button"
                            className="btn btn-sm btn-danger"
                            disabled={pending === m.id}
                            onClick={() => void remove(m.id)}
                          >
                            Forget it
                          </button>
                          <button
                            type="button"
                            className="btn btn-quiet btn-sm"
                            onClick={() => setConfirm(null)}
                          >
                            Keep
                          </button>
                        </div>
                      ) : (
                        <div className="actions">
                          <button
                            type="button"
                            className="btn btn-quiet btn-sm"
                            onClick={() => {
                              setCorrecting(m.id);
                              setCorrection(m.content);
                            }}
                          >
                            Correct
                          </button>
                          <button
                            type="button"
                            className="btn btn-quiet btn-sm"
                            onClick={() => setConfirm(m.id)}
                          >
                            Forget
                          </button>
                        </div>
                      )}
                    </>
                  )}
                </li>
              ))}
            </ul>
          </section>
        );
      })}
      {earlier.length > 0 && (
        <details className="disclosure">
          <summary>Corrected ({earlier.length})</summary>
          <ul className="card rows disclosure-body">
            {earlier.map((m) => (
              <li key={m.id} className="memory-row superseded">
                <div className="memory-text">
                  <p>{m.content}</p>
                  <p className="muted small">
                    Corrected {dateLabel(m.superseded_at)}
                  </p>
                </div>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
