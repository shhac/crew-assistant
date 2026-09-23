import { ProjectDirectories } from "./ProjectForms";
import { ActivityList } from "./OverviewPage";
import { DecisionCard } from "./DecisionCard";
import { BriefCard } from "./ProjectBrief";
import { TeamCard } from "./ProjectTeam";
import { LatestDeliverable } from "./DraftPreview";
import { AskForSomething, TaskList, WorkHistory } from "./ProjectWork";
import { projectStatusLine, projectTasks } from "./taskStatus";
import { PageHeading, Status } from "./ui";
import { pendingDecisions, type Project, type State } from "./api";

export function ProjectDetail({
  project,
  state,
  onBack,
  refresh,
}: {
  project: Project;
  state: State;
  onBack: () => void;
  refresh: () => Promise<void>;
}) {
  const tasks = projectTasks(project, state.tasks);
  const status = projectStatusLine(project, state.tasks);
  const decisions = pendingDecisions(state.decisions).filter(
    (d) => d.project_id === project.id,
  );
  const projectState = {
    ...state,
    activity: state.activity.filter((a) => a.project_id === project.id),
  };
  return (
    <section>
      <button className="text-button back-link" onClick={onBack}>
        ← All projects
      </button>
      <PageHeading
        eyebrow="PROJECT"
        title={project.title}
        description={project.brief.goal || "No brief yet."}
        action={<Status tone={status.tone}>{status.label}</Status>}
      />
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
      <div className="project-sections">
        <LatestDeliverable project={project} tasks={tasks} />
        <AskForSomething key={project.id} project={project} refresh={refresh} />
        <section className="project-card" aria-label="Asked for">
          <div className="section-heading">
            <h2>Asked for {tasks.length > 0 && <span>{tasks.length}</span>}</h2>
          </div>
          <TaskList
            tasks={tasks}
            hasTeam={!!project.playbook}
            refresh={refresh}
          />
          <WorkHistory project={project} tasks={tasks} />
        </section>
        <BriefCard
          key={`brief-${project.id}`}
          project={project}
          refresh={refresh}
        />
        <TeamCard
          key={`team-${project.id}`}
          project={project}
          refresh={refresh}
        />
      </div>
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
        <p className="field-hint">For diagnostics and external integrations.</p>
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
