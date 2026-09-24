import { useState, type FormEvent } from "react";
import { requestHref, projectHref } from "./router";
import { approveLabel, isCode, reversibility, whatHappens } from "./stages";
import { ErrorNotice, Pill, sinceLabel } from "./ui";
import { api, errorText, type Decision, type Project, type Task } from "./api";

const kindLabel: Record<string, string> = {
  delivery: "Ready to approve",
  question: "Question",
  escalation: "Out of rounds",
  failure: "Stuck",
};

const outcomeLabel: Record<string, string> = {
  pass: "Passed",
  revise: "Asked for changes",
  question: "Asked a question",
};

/** Choices whose words the owner needs to add: picking one opens the answer. */
const withWords = new Set(["Request changes"]);

/** Server choices shown in the owner's terms; the choice sent is unchanged. */
function choiceLabel(
  choice: string,
  decision: Decision,
  task?: Task,
  project?: Project,
) {
  if (decision.kind === "delivery" && choice === "Approve")
    return approveLabel(task?.playbook ?? project?.playbook);
  if (choice === "Stop") return "Stop the request";
  if (choice === "Use your judgment" || choice === "Use your judgement")
    return "Let the team decide";
  if (choice === "Accept this draft") return "Accept it as it is";
  return choice;
}

/**
 * One decision the owner is asked for. Compact on the inbox; in full, with
 * the evidence and what approving does, beside the request it holds.
 */
export function DecisionCard({
  decision,
  project,
  task,
  refresh,
  full = false,
}: {
  decision: Decision;
  project?: Project;
  task?: Task;
  refresh: () => Promise<void>;
  full?: boolean;
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [mode, setMode] = useState<"answer" | "dismiss" | "">(
    decision.kind === "question" ? "answer" : "",
  );
  const [draft, setDraft] = useState("");
  const playbook = task?.playbook ?? project?.playbook;
  const delivery = decision.kind === "delivery";
  async function send(value: string, action: "choice" | "answer" | "dismiss") {
    setBusy(action === "choice" ? value : action);
    setError("");
    try {
      await api(
        `/api/decisions/${encodeURIComponent(decision.id)}/${action === "dismiss" ? "dismiss" : "resolve"}`,
        {
          method: "POST",
          body: JSON.stringify(
            action === "dismiss"
              ? { reason: value }
              : action === "answer"
                ? { answer: value }
                : { choice: value },
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
  function submit(e: FormEvent) {
    e.preventDefault();
    if (draft.trim() && !busy && mode) void send(draft.trim(), mode);
  }
  const showRecommendation =
    !!decision.recommendation &&
    decision.kind !== "delivery" &&
    decision.kind !== "question" &&
    decision.kind !== "failure";
  const choices = decision.choices ?? [];
  const answerPrompt =
    decision.kind === "question"
      ? "Your answer"
      : delivery
        ? "What should change?"
        : "Your answer";
  const latest = task?.revisions?.at(-1);
  const evidence =
    full && latest
      ? (task?.verdicts ?? []).filter((v) => v.revision === latest.n)
      : [];
  return (
    <article className={`decision card${full ? " decision-full" : ""}`}>
      <div className="decision-meta">
        {!full && (
          <Pill tone="needs" dot>
            {kindLabel[decision.kind ?? ""] ?? "Decision"}
          </Pill>
        )}
        {project && !full && (
          <a href={projectHref(project.id)}>{project.title}</a>
        )}
        {decision.created_at && (
          <span className="muted small">{sinceLabel(decision.created_at)}</span>
        )}
        {delivery && isCode(playbook) && (
          <span className="decision-reversible">
            <Pill tone="wait">{reversibility(playbook?.land)}</Pill>
          </span>
        )}
      </div>
      <h3 className="decision-title">{decision.title}</h3>
      {decision.context && (
        <p className={`decision-context${full ? "" : " clamp"}`}>
          {decision.context}
        </p>
      )}
      {showRecommendation && (
        <p className="decision-recommendation">
          <span className="label">Recommended</span> {decision.recommendation}
        </p>
      )}
      {full && evidence.length > 0 && (
        <section className="decision-section" aria-label="Checks">
          <h4 className="label">Checks on draft {latest?.n}</h4>
          <ul className="evidence">
            {evidence.map((v, i) => (
              <li key={`${v.role}-${i}`}>
                <Pill tone={v.outcome === "pass" ? "done" : "needs"}>
                  {outcomeLabel[v.outcome] ?? v.outcome}
                </Pill>
                <span>
                  <strong>{v.role}</strong> {v.summary}
                </span>
              </li>
            ))}
            {!!latest?.files?.length && isCode(playbook) && (
              <li>
                <Pill>{latest.files.length} files</Pill>
                <span className="muted">changed in this draft</span>
              </li>
            )}
          </ul>
        </section>
      )}
      {full && delivery && (
        <section className="decision-section" aria-label="When you approve">
          <h4 className="label">When you approve</h4>
          <ol className="happens">
            {whatHappens(playbook).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ol>
        </section>
      )}
      {mode ? (
        <form className="decision-answer" onSubmit={submit}>
          <label className="control">
            {mode === "dismiss"
              ? "Why close it without deciding?"
              : answerPrompt}
            <textarea
              className="field"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              maxLength={mode === "dismiss" ? 4096 : 16384}
              disabled={!!busy}
              rows={2}
              required
            />
          </label>
          <div className="actions">
            <button
              className="btn btn-primary"
              type="submit"
              disabled={!!busy || !draft.trim()}
            >
              {mode === "dismiss"
                ? "Close it"
                : delivery
                  ? "Send changes"
                  : "Send answer"}
            </button>
            {choices
              .filter(
                (c) =>
                  mode === "answer" &&
                  decision.kind === "question" &&
                  !withWords.has(c),
              )
              .map((choice) => (
                <button
                  key={choice}
                  type="button"
                  className="btn btn-quiet"
                  disabled={!!busy}
                  onClick={() => void send(choice, "choice")}
                >
                  {choiceLabel(choice, decision, task, project)}
                </button>
              ))}
            {decision.kind === "question" && mode === "answer" ? (
              <button
                type="button"
                className="btn btn-quiet decision-close"
                disabled={!!busy}
                onClick={() => {
                  setMode("dismiss");
                  setDraft("");
                }}
              >
                Close without deciding
              </button>
            ) : (
              <button
                type="button"
                className="btn btn-quiet"
                disabled={!!busy}
                onClick={() =>
                  setMode(decision.kind === "question" ? "answer" : "")
                }
              >
                Cancel
              </button>
            )}
          </div>
        </form>
      ) : (
        <div className="actions">
          {choices.map((choice, i) => (
            <button
              key={choice}
              type="button"
              className={`btn${i === 0 ? " btn-primary" : ""}`}
              disabled={!!busy}
              onClick={() =>
                withWords.has(choice)
                  ? setMode("answer")
                  : void send(choice, "choice")
              }
            >
              {choiceLabel(choice, decision, task, project)}
            </button>
          ))}
          {!delivery &&
            decision.kind !== "failure" &&
            decision.kind !== "escalation" && (
              <button
                type="button"
                className="btn btn-quiet"
                disabled={!!busy}
                onClick={() => setMode("answer")}
              >
                Answer in your own words
              </button>
            )}
          {!full && task && (
            <a
              className="btn btn-quiet"
              href={requestHref(task.project_id, task.id)}
            >
              Review
            </a>
          )}
          <button
            type="button"
            className="btn btn-quiet decision-close"
            disabled={!!busy}
            onClick={() => {
              setMode("dismiss");
              setDraft("");
            }}
          >
            Close without deciding
          </button>
        </div>
      )}
      <ErrorNotice error={error} />
    </article>
  );
}
