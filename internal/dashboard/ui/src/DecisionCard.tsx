import { useState, type FormEvent } from "react";
import { requestHref, projectHref } from "./router";
import {
  decisionKind,
  isCode,
  taskPlaybook,
  type DecisionKindWords,
} from "./stages";
import { reversibility } from "./landing";
import { ErrorNotice, Pill, sinceLabel, useAction } from "./ui";
import {
  dismissDecision,
  resolveDecision,
  type Decision,
  type Playbook,
  type Project,
  type Task,
} from "./api";

/** Choices whose words the owner needs to add: picking one opens the answer. */
const withWords = new Set(["Request changes"]);

/** Server choices shown in the owner's terms; the choice sent is unchanged. */
function choiceLabel(
  choice: string,
  kind: DecisionKindWords,
  playbook?: Playbook,
) {
  if (choice === "Approve" && kind.approve) return kind.approve(playbook);
  if (choice === "Stop") return "Stop request";
  if (choice === "Use your judgment" || choice === "Use your judgement")
    return "Let the team decide";
  if (choice === "Accept this draft") return "Accept it as it is";
  return choice;
}

const resolution = {
  choice: (value: string) => ({ choice: value }),
  answer: (value: string) => ({ answer: value }),
};

type Mode = "answer" | "dismiss";

/**
 * One decision the owner is asked for: compact on the inbox, in full beside
 * the request it holds. A request's decision is never closed without an
 * answer, since that would stop the request; "Stop request" says so.
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
  const kind = decisionKind(decision);
  const alwaysOpen = kind.answering === "open";
  const { busy, error, run } = useAction();
  const [mode, setMode] = useState<Mode | "">(alwaysOpen ? "answer" : "");
  const [draft, setDraft] = useState("");
  const playbook = taskPlaybook(task, project);
  const closable = !task;
  const asking = alwaysOpen && mode === "answer";
  async function send(value: string, action: "choice" | Mode) {
    await run(async () => {
      await (action === "dismiss"
        ? dismissDecision(decision.id, value)
        : resolveDecision(decision.id, resolution[action](value)));
      await refresh();
    });
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    if (draft.trim() && !busy && mode) void send(draft.trim(), mode);
  }
  const forms: Record<Mode, { prompt: string; send: string; max: number }> = {
    answer: { prompt: kind.prompt, send: kind.send, max: 16384 },
    dismiss: {
      prompt: "Why close it without deciding?",
      send: "Close it",
      max: 4096,
    },
  };
  const choices = decision.choices ?? [];
  const label = (choice: string) => choiceLabel(choice, kind, playbook);
  const closeWithoutDeciding = closable && (
    <button
      type="button"
      className="btn btn-quiet decision-close"
      disabled={busy}
      onClick={() => {
        setMode("dismiss");
        setDraft("");
      }}
    >
      Close without deciding
    </button>
  );
  return (
    <article className={`decision card${full ? " decision-full" : ""}`}>
      <div className="decision-meta">
        {!full && (
          <Pill tone="needs" dot>
            {kind.badge}
          </Pill>
        )}
        {project && !full && (
          <a href={projectHref(project.id)}>{project.title}</a>
        )}
        {decision.created_at && (
          <span className="muted small">{sinceLabel(decision.created_at)}</span>
        )}
        {kind.reversible && isCode(playbook) && (
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
      {!!decision.recommendation && kind.recommend && (
        <p className="decision-recommendation">
          <span className="label">Recommended</span> {decision.recommendation}
        </p>
      )}
      {mode ? (
        <form className="decision-answer" onSubmit={submit}>
          <label className="control">
            {forms[mode].prompt}
            <textarea
              className="field"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              maxLength={forms[mode].max}
              disabled={busy}
              rows={2}
              required
            />
          </label>
          <div className="actions">
            <button
              className="btn btn-primary"
              type="submit"
              disabled={busy || !draft.trim()}
            >
              {forms[mode].send}
            </button>
            {asking &&
              choices
                .filter((c) => !withWords.has(c))
                .map((choice) => (
                  <button
                    key={choice}
                    type="button"
                    className="btn btn-quiet"
                    disabled={busy}
                    onClick={() => void send(choice, "choice")}
                  >
                    {label(choice)}
                  </button>
                ))}
            {asking ? (
              closeWithoutDeciding
            ) : (
              <button
                type="button"
                className="btn btn-quiet"
                disabled={busy}
                onClick={() => setMode(alwaysOpen ? "answer" : "")}
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
              disabled={busy}
              onClick={() =>
                withWords.has(choice)
                  ? setMode("answer")
                  : void send(choice, "choice")
              }
            >
              {label(choice)}
            </button>
          ))}
          {kind.answering === "offered" && (
            <button
              type="button"
              className="btn btn-quiet"
              disabled={busy}
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
          {closeWithoutDeciding}
        </div>
      )}
      <ErrorNotice error={error} />
    </article>
  );
}
