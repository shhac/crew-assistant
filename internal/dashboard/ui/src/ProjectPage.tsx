import { Board } from "./Board";
import { BriefTab } from "./ProjectBrief";
import { TeamTab } from "./ProjectTeam";
import { LandingTab } from "./ProjectLanding";
import { ActivityTab } from "./ProjectActivity";
import { RequestPanel } from "./RequestPanel";
import { href, projectHref, type ProjectTab, type Route } from "./router";
import { isCode, landsBy, needsYou, projectKind, projectTasks } from "./stages";
import { Icon, Pill } from "./ui";
import type { Project, State } from "./api";

const tabLabels: Record<ProjectTab, string> = {
  board: "Board",
  brief: "Brief",
  team: "Team",
  landing: "Landing",
  activity: "Activity",
};

export function ProjectPage({
  project,
  route,
  state,
  refresh,
}: {
  project: Project;
  route: Extract<Route, { page: "project" }>;
  state: State;
  refresh: () => Promise<void>;
}) {
  const tasks = projectTasks(project, state.tasks);
  const waiting = tasks.filter(needsYou).length;
  const code = isCode(project.playbook);
  const tabs: ProjectTab[] = code
    ? ["board", "brief", "team", "landing", "activity"]
    : ["board", "brief", "team", "activity"];
  const tab = tabs.includes(route.tab) ? route.tab : "board";
  const request = route.request
    ? tasks.find((t) => t.id === route.request)
    : undefined;
  const folder = project.playbook?.repo || project.directories?.[0];
  return (
    <div className="page project-page">
      <header className="page-header project-header">
        <nav className="crumbs" aria-label="Breadcrumb">
          <a href={href({ page: "projects" })}>Projects</a>
          <span aria-hidden="true">/</span>
          <span aria-current="page">{project.title}</span>
        </nav>
        <div className="project-title-row">
          <h1>{project.title}</h1>
          {waiting > 0 && (
            <Pill tone="needs" dot>
              {waiting} need{waiting === 1 ? "s" : ""} you
            </Pill>
          )}
        </div>
        <p className="project-meta soft">
          {code && <Icon name="Branch" size={14} />}
          <span>{projectKind(project.playbook)}</span>
          {folder && (
            <>
              <span aria-hidden="true">·</span>
              <code title={folder}>
                {folder.split(/[\\/]/).filter(Boolean).pop()}
              </code>
            </>
          )}
          {project.playbook && (
            <>
              <span aria-hidden="true">·</span>
              <span>{landsBy(project.playbook)}</span>
            </>
          )}
        </p>
        <nav className="tabs" aria-label="Project">
          {tabs.map((t) => (
            <a
              key={t}
              className="tab"
              href={projectHref(project.id, t)}
              aria-current={tab === t ? "page" : undefined}
            >
              {tabLabels[t]}
            </a>
          ))}
        </nav>
      </header>
      {tab === "board" && (
        <Board project={project} state={state} refresh={refresh} />
      )}
      {tab === "brief" && <BriefTab project={project} refresh={refresh} />}
      {tab === "team" && (
        <TeamTab project={project} members={state.members} refresh={refresh} />
      )}
      {tab === "landing" && <LandingTab project={project} refresh={refresh} />}
      {tab === "activity" && <ActivityTab project={project} state={state} />}
      {route.request && (
        <RequestPanel
          key={route.request}
          project={project}
          task={request}
          state={state}
          refresh={refresh}
          onClose={() => {
            window.location.hash = projectHref(project.id);
          }}
        />
      )}
    </div>
  );
}
