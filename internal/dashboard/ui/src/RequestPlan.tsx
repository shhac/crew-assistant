import { useState } from "react";
import { sinceLabel } from "./ui";
import type { DesignRequest, Plan } from "./api";

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

/**
 * Each time the researcher or the implementer asked the designer, and what
 * came back: the designer's input, the owner's answer, or nothing yet.
 */
export function RequestDesign({
  design,
  designer,
}: {
  design: DesignRequest[];
  /** Who has the request open now, if anyone. */
  designer?: string;
}) {
  return (
    <section className="section plan" aria-label="Design input">
      <h3>Design input</h3>
      {design.map((r) => (
        <div key={r.id} className="plan-part">
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
        </div>
      ))}
    </section>
  );
}

/** Where a request with no designer's input stands. */
function unanswered(r: DesignRequest, designer?: string) {
  if (r.decision) return r.answered_at ? "You answered it" : "Brought to you";
  return designer ? `With ${designer}` : "Not answered";
}
