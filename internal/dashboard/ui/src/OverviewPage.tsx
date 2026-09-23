import { useState } from "react";
import { ProjectLink } from "./ProjectLink";
import { DecisionCard } from "./DecisionCard";
import { groupActivity } from "./activity";
import { dateLabel, Empty, humanStatus, Icon, PageHeading, Status } from "./ui";
import { pendingDecisions, type Project, type State } from "./api";
import type { Page } from "./navigation";

export function Overview({
  state,
  onNew,
  onNavigate,
  onProject,
  refresh,
}: {
  state: State;
  onNew: () => void;
  onNavigate: (p: Page) => void;
  onProject: (id: string) => void;
  refresh: () => Promise<void>;
}) {
  const [fullActivity, setFullActivity] = useState(false);
  const decisions = pendingDecisions(state.decisions);
  const active = state.projects.filter(
    (p) => !["completed", "cancelled", "archived"].includes(p.status),
  );
  return (
    <section>
      <PageHeading
        eyebrow="ROOM TO FOCUS"
        title="A clear view of the work."
        description={
          state.projects.length
            ? "The outcomes you care about. The decisions that need you."
            : "Give your assistant an outcome. Keep your attention for the decisions that matter."
        }
        action={
          <button className="button primary" onClick={onNew}>
            <Icon name="Plus" size={16} />
            Add project
          </button>
        }
      />
      <NeedsYou
        decisions={decisions.length}
        operations={state.pending_operations.length}
        onReview={() => onNavigate("Decisions")}
      />
      {decisions.length > 0 && (
        <div className="overview-decision">
          {decisions.slice(0, 3).map((decision) => (
            <DecisionCard
              key={decision.id}
              decision={decision}
              projects={state.projects}
              refresh={refresh}
              compact
            />
          ))}
        </div>
      )}
      <section className="section-block">
        <div className="section-heading">
          <h2>
            Projects in motion <span>{active.length}</span>
          </h2>
          {state.projects.length > 0 && (
            <button
              className="text-button"
              onClick={() => onNavigate("Projects")}
            >
              All projects <Icon name="Arrow" size={14} />
            </button>
          )}
        </div>
        {active.length ? (
          <div className="project-list">
            {active.slice(0, 5).map((p) => (
              <ProjectRow
                project={p}
                state={state}
                key={p.id}
                onSelect={() => onProject(p.id)}
              />
            ))}
          </div>
        ) : (
          <Empty
            icon="Projects"
            title={
              state.projects.length
                ? "Room for your next outcome"
                : "Start with the outcome"
            }
            action={
              <button className="button secondary" onClick={onNew}>
                Set up a project <Icon name="Arrow" size={15} />
              </button>
            }
          >
            {state.projects.length
              ? "Your active project list is clear. Completed work remains in Projects."
              : "Describe what should be achieved and what success looks like. Your assistant keeps the context together."}
          </Empty>
        )}
      </section>
      <section className="section-block">
        <div className="section-heading">
          <h2>Behind the scenes</h2>
          <span className="section-note">The work around the work</span>
        </div>
        <ActivityList state={state} limit={fullActivity ? undefined : 5} />
        {state.activity.length > 5 && (
          <button
            type="button"
            className="text-button"
            aria-expanded={fullActivity}
            onClick={() => setFullActivity((open) => !open)}
          >
            {fullActivity ? "Show recent activity" : "View activity"}
          </button>
        )}
      </section>
    </section>
  );
}
export function ProjectRow({
  project,
  state,
  onSelect,
}: {
  project: Project;
  state: State;
  onSelect: () => void;
}) {
  const folders = project.directories?.length ?? 0;
  const decisions = pendingDecisions(state.decisions).filter(
    (d) => d.project_id === project.id,
  );
  return (
    <button className="project-row" onClick={onSelect}>
      <span className="project-symbol">
        <Icon name="Projects" size={19} />
      </span>
      <span className="project-info">
        <strong>{project.title}</strong>
        <span>
          {project.description ||
            "Open to review the desired outcome and acceptance criteria."}
        </span>
        <span className="project-meta">
          {folders
            ? `${folders} linked ${folders === 1 ? "folder" : "folders"}`
            : "No folders linked"}
          {decisions.length > 0 && (
            <span className="decision-meta">
              {" "}
              · {decisions.length} decision{decisions.length !== 1 ? "s" : ""}
            </span>
          )}
        </span>
      </span>
      <span className="project-states">
        <Status
          tone={
            decisions.length
              ? "amber"
              : project.status === "completed"
                ? "green"
                : ""
          }
        >
          {humanStatus(project.status)}
        </Status>
      </span>
      <Icon name="Chevron" size={15} />
    </button>
  );
}

export function ActivityList({
  state,
  limit,
}: {
  state: State;
  limit?: number;
}) {
  const groups = groupActivity(state.activity, limit);
  if (!groups.length)
    return (
      <div className="quiet-activity">
        <span className="activity-line" />
        <p>
          No activity yet.
          <span>Updates will appear here.</span>
        </p>
      </div>
    );
  return (
    <ol className="activity-list">
      {groups.map(({ entry, label, count }) => (
        <li key={entry.id}>
          <span className="activity-dot" />
          <div>
            <p>{entry.summary}</p>
            <span>
              {state.projects.some((p) => p.id === entry.project_id) ? (
                <ProjectLink
                  project={state.projects.find(
                    (p) => p.id === entry.project_id,
                  )!}
                />
              ) : (
                label
              )}
              {count > 1 && ` · ${count} updates`}
              {entry.created_at && (
                <>
                  {" "}
                  ·{" "}
                  <time dateTime={entry.created_at}>
                    {dateLabel(entry.created_at)}
                  </time>
                </>
              )}
            </span>
          </div>
        </li>
      ))}
    </ol>
  );
}

function NeedsYou({
  decisions,
  operations,
  onReview,
}: {
  decisions: number;
  operations: number;
  onReview: () => void;
}) {
  const waiting = decisions + operations;
  const parts = [
    decisions && `${decisions} decision${decisions === 1 ? "" : "s"}`,
    operations &&
      `${operations} interrupted operation${operations === 1 ? "" : "s"}`,
  ].filter(Boolean);
  return (
    <div className={`attention-banner ${waiting ? "needs-attention" : ""}`}>
      <span className="attention-symbol">
        <Icon name={waiting ? "Decisions" : "Check"} size={23} />
      </span>
      <div>
        <h2>
          {waiting
            ? `${parts.join(" and ")} waiting on you`
            : "Nothing needs you right now"}
        </h2>
        <p>
          {waiting
            ? "Each one comes with context and a recommendation."
            : "When your judgment is needed, it will appear here first."}
        </p>
      </div>
      {waiting > 0 && (
        <button className="text-button" onClick={onReview}>
          Review <Icon name="Arrow" size={16} />
        </button>
      )}
    </div>
  );
}
