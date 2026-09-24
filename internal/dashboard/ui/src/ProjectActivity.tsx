import { useState } from "react";
import { groupActivity, isRoutineActivity } from "./activity";
import { dateLabel } from "./ui";
import type { Project, State } from "./api";

export function ActivityTab({
  project,
  state,
}: {
  project: Project;
  state: State;
}) {
  const [everything, setEverything] = useState(false);
  const own = state.activity.filter((a) => a.project_id === project.id);
  const routine = own.filter((a) => isRoutineActivity(a.kind)).length;
  const shown = groupActivity(
    everything ? own : own.filter((a) => !isRoutineActivity(a.kind)),
  );
  return (
    <section className="tab-panel card" aria-label="Activity">
      <div className="panel-head">
        <h2>Activity</h2>
        {routine > 0 && (
          <button
            type="button"
            className="btn btn-quiet btn-sm"
            onClick={() => setEverything(!everything)}
          >
            {everything
              ? "Hide steps of work"
              : `Show every step (${routine} more)`}
          </button>
        )}
      </div>
      {shown.length ? (
        <ol className="activity">
          {shown.map(({ entry, label, count }) => (
            <li key={entry.id}>
              <time className="muted small" dateTime={entry.created_at}>
                {dateLabel(entry.created_at)}
              </time>
              <span className="activity-kind label">{label}</span>
              <span>
                {entry.summary}
                {count > 1 && (
                  <span className="muted small"> ({count} times)</span>
                )}
              </span>
            </li>
          ))}
        </ol>
      ) : (
        <p className="muted">Nothing yet.</p>
      )}
    </section>
  );
}
