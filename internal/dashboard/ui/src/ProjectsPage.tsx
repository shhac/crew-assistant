import { ProjectDirectories } from "./ProjectForms";
import { ActivityList, ProjectRow } from "./OverviewPage";
import { DecisionCard } from "./DecisionCard";
import {
  CriteriaList,
  Empty,
  humanStatus,
  Icon,
  PageHeading,
  Status,
} from "./ui";
import { pendingDecisions, type State } from "./api";

export function Projects({
  state,
  selected,
  onSelect,
  onNew,
  refresh,
}: {
  state: State;
  selected: string | null;
  onSelect: (id: string | null) => void;
  onNew: () => void;
  refresh: () => Promise<void>;
}) {
  const project = state.projects.find((p) => p.id === selected);
  if (project) {
    const projectState = {
      ...state,
      activity: state.activity.filter((a) => a.project_id === project.id),
    };
    const decisions = pendingDecisions(state.decisions).filter(
      (d) => d.project_id === project.id,
    );
    return (
      <section>
        <button
          className="text-button back-link"
          onClick={() => onSelect(null)}
        >
          ← All projects
        </button>
        <PageHeading
          eyebrow="PROJECT CONTEXT"
          title={project.title}
          description={project.description}
          action={<Status>{humanStatus(project.status)}</Status>}
        />
        <p className="section-description">
          Team work for this project will appear here.
        </p>
        {decisions.length > 0 && (
          <section
            className="section-block"
            aria-label="Decisions for this project"
          >
            <div className="section-heading">
              <h2>
                Decisions <span>{decisions.length}</span>
              </h2>
            </div>
            <div className="decision-list">
              {decisions.map((decision) => (
                <DecisionCard
                  key={decision.id}
                  decision={decision}
                  projects={state.projects}
                  refresh={refresh}
                  compact
                />
              ))}
            </div>
          </section>
        )}
        <section className="detail-section">
          <p className="eyebrow">WHAT DONE LOOKS LIKE</p>
          <CriteriaList
            criteria={project.acceptance_criteria}
            empty="No acceptance criteria recorded."
            marker
          />
        </section>
        <ProjectDirectories
          key={project.id}
          project={project}
          refresh={refresh}
        />
        <details className="project-setup-details">
          <summary>Technical identifiers</summary>
          <label htmlFor="project-setup-id">
            Project ID
            <input
              id="project-setup-id"
              value={project.id}
              readOnly
              onFocus={(e) => e.target.select()}
            />
          </label>
          <p className="field-hint">
            For diagnostics and external integrations.
          </p>
        </details>
        <section className="section-block">
          <div className="section-heading">
            <h2>Activity</h2>
          </div>
          <ActivityList state={projectState} />
        </section>
      </section>
    );
  }
  return (
    <section>
      <PageHeading
        eyebrow="OUTCOMES, WITH OWNERSHIP"
        title="Projects"
        description="What you're moving forward, and what done looks like."
        action={
          <button className="button primary" onClick={onNew}>
            <Icon name="Plus" size={16} />
            Add project
          </button>
        }
      />
      {state.projects.length ? (
        <div className="project-list">
          {state.projects.map((p) => (
            <ProjectRow
              key={p.id}
              project={p}
              state={state}
              onSelect={() => onSelect(p.id)}
            />
          ))}
        </div>
      ) : (
        <Empty
          icon="Projects"
          title="One outcome is a good start"
          action={
            <button className="button primary" onClick={onNew}>
              Add project <Icon name="Arrow" size={15} />
            </button>
          }
        >
          Give the work a name, describe the outcome, and define how you'll know
          it's complete.
        </Empty>
      )}
    </section>
  );
}
