import { useState } from "react";
import { ProjectLink } from "./ProjectLink";
import { ErrorNotice, Icon } from "./ui";
import { api, errorText, type Decision, type Project } from "./api";

export function DecisionCard({
  decision,
  projects,
  refresh,
  compact = false,
}: {
  decision: Decision;
  projects: Project[];
  refresh: () => Promise<void>;
  compact?: boolean;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [mode, setMode] = useState<"answer" | "dismiss" | "">("");
  const [draft, setDraft] = useState("");
  async function choose(
    choice: string,
    action: "choice" | "answer" | "dismiss" = "choice",
  ) {
    setBusy(action === "choice" ? choice : action);
    setError("");
    try {
      await api(
        `/api/decisions/${encodeURIComponent(decision.id)}/${action === "dismiss" ? "dismiss" : "resolve"}`,
        {
          method: "POST",
          body: JSON.stringify(
            action === "dismiss"
              ? { reason: choice }
              : action === "answer"
                ? { answer: choice }
                : { choice },
          ),
        },
      );
      await refresh();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  }
  const project = projects.find((p) => p.id === decision.project_id);
  return (
    <article className={`decision-card ${compact ? "compact" : ""}`}>
      <div className="decision-topline">
        <span className="eyebrow">YOUR DECISION</span>
        {project && <ProjectLink project={project} />}
      </div>
      <h3>{decision.title}</h3>
      <p className="decision-context">{decision.context}</p>
      {decision.recommendation && (
        <div className="recommendation">
          <Icon name="Arrow" size={17} />
          <div>
            <strong>Recommendation</strong>
            <p>{decision.recommendation}</p>
          </div>
        </div>
      )}
      <div className="decision-actions">
        {(decision.choices || []).map((choice, index) => (
          <button
            className={`button ${index === 0 ? "warm" : "secondary"}`}
            disabled={!!busy}
            key={choice}
            onClick={() => void choose(choice)}
          >
            {busy === choice ? "Recording…" : choice}
            {index === 0 && <Icon name="Arrow" size={14} />}
          </button>
        ))}
        {!decision.choices?.length && (
          <p className="muted">
            Reply in the conversation to discuss this decision.
          </p>
        )}
      </div>
      <div className="decision-actions">
        <button
          className="button secondary"
          disabled={!!busy}
          onClick={() => {
            setMode("answer");
            setDraft("");
            setError("");
          }}
        >
          Give a different answer
        </button>
        <button
          className="button secondary"
          disabled={!!busy}
          onClick={() => {
            setMode("dismiss");
            setDraft("");
            setError("");
          }}
        >
          No longer needed
        </button>
      </div>
      {mode && (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (draft.trim() && !busy) void choose(draft.trim(), mode);
          }}
        >
          <label>
            {mode === "dismiss"
              ? "Why is this no longer needed?"
              : "Your answer"}
            <textarea
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              maxLength={mode === "dismiss" ? 4096 : 16384}
              disabled={!!busy}
              required
              rows={3}
            />
          </label>
          {mode === "dismiss" && (
            <p className="muted">
              Closes this question and records your reason. It does not approve
              or restart any work.
            </p>
          )}
          <div className="decision-actions">
            <button
              className="button warm"
              disabled={!!busy || !draft.trim()}
              type="submit"
            >
              {busy
                ? "Recording…"
                : mode === "dismiss"
                  ? "Dismiss decision"
                  : "Record answer"}
            </button>
            <button
              className="button secondary"
              disabled={!!busy}
              type="button"
              onClick={() => setMode("")}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
      <ErrorNotice error={error} />
    </article>
  );
}
