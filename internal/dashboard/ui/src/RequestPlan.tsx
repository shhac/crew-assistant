import { useState } from "react";
import { sinceLabel } from "./ui";
import type { Plan } from "./api";

/** Beyond this many points, the plan opens folded to its summary. */
const foldAfter = 3;

/** What the planner worked out before anything was written. */
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
        Planned by {plan.role}
        {when && ` · ${when}`}
      </p>
    </section>
  );
}
