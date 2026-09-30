import { useMemo, useState } from "react";
import {
  activityDayKey,
  activityLabel,
  foldActivity,
  isRoutineActivity,
  pageActivity,
  type ActivityFilter,
} from "./activity";
import { TaskRef, recordedTime, sinceLabel } from "./ui";
import type { Activity, Project, State } from "./api";

const filters: [ActivityFilter, string][] = [
  ["all", "All"],
  ["needs", "Needs you"],
  ["outcomes", "Outcomes"],
  ["steps", "Steps"],
];

export function ActivityTab({
  project,
  state,
}: {
  project: Project;
  state: State;
}) {
  const [filter, setFilter] = useState<ActivityFilter>("all");
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const own = useMemo(
    () => state.activity.filter((a) => a.project_id === project.id),
    [state.activity, project.id],
  );
  const tasks = useMemo(
    () => new Map(state.tasks.map((t) => [t.id, t])),
    [state.tasks],
  );
  const now = new Date();
  // Day labels update on each polling render, including across midnight.
  const all = useMemo(
    () => foldActivity(own, "all", now),
    [own, activityDayKey(now)],
  );
  const initial = pageActivity(all, now);
  const [loadedThrough, setLoadedThrough] = useState<string | undefined>(
    () => initial.oldestKey,
  );
  const cutoff = loadedThrough || initial.oldestKey;
  const end = all.findIndex((day) => day.key === cutoff);
  const loaded = new Set(all.slice(0, end + 1).map((day) => day.key));
  const filtered = useMemo(
    () => (filter === "all" ? all : foldActivity(own, filter, now)),
    [own, all, filter],
  );
  const shown = filtered.filter((day) => loaded.has(day.key));
  const hasOlder = end < all.length - 1;
  const time = (entry: Activity, today: boolean) => (
    <time className="muted small" dateTime={entry.created_at}>
      {today
        ? sinceLabel(entry.created_at)
        : recordedTime(entry.created_at)?.toLocaleTimeString(undefined, {
            hour: "numeric",
            minute: "2-digit",
          }) || ""}
    </time>
  );
  const label = (entry: Activity) => (
    <span
      className={
        isRoutineActivity(entry.kind)
          ? "activity-kind label muted"
          : "activity-kind label"
      }
    >
      {activityLabel(entry.kind)}
    </span>
  );
  return (
    <section className="tab-panel card project-activity" aria-label="Activity">
      <div className="panel-head">
        <h2>Activity</h2>
      </div>
      <div
        className="segmented role-filter"
        role="group"
        aria-label="Activity filters"
      >
        {filters.map(([value, title]) => (
          <button
            key={value}
            type="button"
            aria-pressed={filter === value}
            onClick={() => setFilter(value)}
          >
            {title}
          </button>
        ))}
      </div>
      {shown.map((day) => (
        <section className="activity-day" key={day.key} aria-label={day.label}>
          <h3 className="label muted">{day.label}</h3>
          <ol className="activity-rows">
            {day.rows.map((row) => {
              const task = row.taskId ? tasks.get(row.taskId) : undefined;
              const open = expanded.has(row.key);
              const context = (
                <span className="activity-context">
                  <TaskRef task={task} />
                  <span className="activity-objective">{task?.objective}</span>
                </span>
              );
              const content = (
                <>
                  {context}
                  <span className="activity-event">
                    {label(row.entry)} <span>{row.entry.summary}</span>
                  </span>
                  {time(row.entry, day.label === "Today")}
                  {row.others > 0 && (
                    <>
                      <span className="muted small">+{row.others} updates</span>
                      <span className="activity-chevron" aria-hidden="true">
                        ›
                      </span>
                    </>
                  )}
                </>
              );
              return (
                <li key={row.key}>
                  {row.others > 0 ? (
                    <button
                      type="button"
                      className="activity-row"
                      aria-expanded={open}
                      onClick={() =>
                        setExpanded((previous) => {
                          const next = new Set(previous);
                          if (next.has(row.key)) next.delete(row.key);
                          else next.add(row.key);
                          return next;
                        })
                      }
                    >
                      {content}
                    </button>
                  ) : (
                    <div className="activity-row">{content}</div>
                  )}
                  {open && row.others > 0 && (
                    <ol className="activity-details">
                      {row.entries.map((entry) => (
                        <li key={entry.id}>
                          {time(entry, day.label === "Today")}
                          {label(entry)}
                          <span>{entry.summary}</span>
                        </li>
                      ))}
                    </ol>
                  )}
                </li>
              );
            })}
          </ol>
        </section>
      ))}
      {!shown.length && (
        <p className="muted">
          {!own.length
            ? "Nothing yet."
            : hasOlder
              ? "Nothing here in the loaded activity; older history remains"
              : "No matching activity."}
        </p>
      )}
      {hasOlder && (
        <button
          type="button"
          className="btn btn-quiet activity-older"
          onClick={() =>
            setLoadedThrough(pageActivity(all, now, cutoff).oldestKey)
          }
        >
          Show older
        </button>
      )}
    </section>
  );
}
