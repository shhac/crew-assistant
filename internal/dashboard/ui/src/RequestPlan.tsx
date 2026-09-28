import { useState } from "react";
import { Pill, sinceLabel } from "./ui";
import {
  AttachmentList,
  designState,
  designStateLabel,
} from "./RequestAttachments";
import type { DesignRequest, Plan, ResearchRequest, Task } from "./api";

/** Beyond this many points, the plan opens folded to its summary. */
const foldAfter = 3;

/** What the researcher worked out before anything was written. */
export function RequestPlan({ plan }: { plan: Plan }) {
  const parts = [
    { label: "What exists", items: plan.exists ?? [] },
    { label: "What will change", items: plan.changes ?? [] },
    { label: "Out of scope", items: plan.out_of_scope ?? [] },
  ].filter((p) => p.items.length > 0);
  const points = parts.reduce((n, p) => n + p.items.length, 0);
  const [open, setOpen] = useState(points <= foldAfter);
  const when = sinceLabel(plan.at);
  return (
    <section className="section plan" aria-label="Plan">
      <h3>Plan</h3>
      <p>{plan.summary}</p>
      {open ? (
        parts.map((p) => (
          <div key={p.label} className="plan-part">
            <p className="label">{p.label}</p>
            <ul className="plan-list">
              {p.items.map((item, i) => (
                <li key={i}>{item}</li>
              ))}
            </ul>
          </div>
        ))
      ) : (
        <button
          type="button"
          className="link-button small plan-more"
          onClick={() => setOpen(true)}
        >
          Show the plan
        </button>
      )}
      <p className="muted small">
        Researched by {plan.role}
        {when && ` · ${when}`}
      </p>
    </section>
  );
}

const designTone = { current: "done", superseded: "", advice: "wait" };

/**
 * Each time the researcher or the implementer asked the designer, and what
 * came back: the designer's input, the owner's answer, or nothing yet. Each
 * input is numbered, with its files, and says whether it is the current
 * design everyone works to, superseded by it, or advice.
 */
export function RequestDesign({
  task,
  designer,
}: {
  task: Task;
  /** Who has the request open now, if anyone. */
  designer?: string;
}) {
  const attachments = task.attachments ?? [];
  return (
    <section className="section plan" aria-label="Design input">
      <h3>Design input</h3>
      {(task.design ?? []).map((r) => {
        const state = designState(task, r);
        return (
          <div
            key={r.id}
            className={`plan-part${state === "superseded" ? " superseded" : ""}`}
            aria-label={r.n ? `Design ${r.n}` : undefined}
          >
            {r.n ? (
              <p className="design-title">
                <span className="label">Design {r.n}</span>
                {state && (
                  <Pill tone={designTone[state]}>
                    {designStateLabel[state]}
                  </Pill>
                )}
              </p>
            ) : null}
            <p className="label">{r.from} asked</p>
            <p>{r.question}</p>
            {r.input ? (
              <>
                <p className="label">{r.designer} answered</p>
                <p>{r.input}</p>
              </>
            ) : (
              <p className="muted small">{unanswered(r, designer)}</p>
            )}
            <AttachmentList
              task={task}
              attachments={attachments.filter((a) => a.design === r.id)}
            />
            {state === "superseded" && (
              <p className="muted small">
                Not the current design: the team no longer works to it.
              </p>
            )}
          </div>
        );
      })}
    </section>
  );
}

/**
 * Each time a checker sent the request back to the researcher, and where
 * that stands: researched and back with the checker, with you, or still
 * being researched.
 */
export function RequestResearch({ research }: { research: ResearchRequest[] }) {
  return (
    <section className="section plan" aria-label="Research asked for">
      <h3>Research asked for</h3>
      {research.map((r) => (
        <div key={r.id} className="plan-part">
          <p className="label">
            {r.from} asked, checking draft {r.revision}
          </p>
          <p>{r.question}</p>
          <p className="muted small">{researchState(r)}</p>
        </div>
      ))}
    </section>
  );
}

function researchState(r: ResearchRequest) {
  if (r.decision) return "Brought to you";
  if (r.answered_at)
    return `${r.researcher ?? "The researcher"} updated the plan; back with ${r.from}`;
  return "Being researched";
}

/** Where a request with no designer's input stands. */
function unanswered(r: DesignRequest, designer?: string) {
  if (r.decision) return r.answered_at ? "You answered it" : "Brought to you";
  return designer ? `With ${designer}` : "Not answered";
}
