import { Board } from "./Board";
import { BriefTab } from "./ProjectBrief";
import { TeamTab } from "./ProjectTeam";
import { ConfigTab } from "./ProjectConfig";
import { ActivityTab } from "./ProjectActivity";
import { RequestPanel } from "./RequestPanel";
import {
  href,
  projectHref,
  projectTabs,
  requestHref,
  seatHref,
  type ProjectTab,
  type Route,
} from "./router";
import {
  findRequest,
  isCode,
  needsYou,
  projectKind,
  projectTasks,
} from "./stages";
import { landsBy } from "./landing";
import { ErrorNotice, useAction, Icon, Pill } from "./ui";
import { setProjectPaused, type Project, type State } from "./api";

const tabLabels: Record<ProjectTab, string> = {
  board: "Board",
  brief: "Brief",
  team: "Team",
  config: "Config",
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
  const { busy, error, run } = useAction();
  const tasks = projectTasks(project, state.tasks);
  const waiting = tasks.filter(needsYou).length;
  const code = isCode(project.playbook);
  const tab = route.tab;
  const request = route.request ? findRequest(tasks, route.request) : undefined;
  const folder = project.playbook?.repo || project.directories?.[0];
  return (
    <div className="page project-page">
      <header className="page-header project-header">
        <nav className="crumbs" aria-label="Breadcrumb">
          <a href={href({ page: "projects" })}>Projects</a>
          <span aria-hidden="true">/</span>
          <span aria-current="page">{project.title}</span>
        </nav>
        <div className="page-header page-header-actions">
          <div className="project-title-row">
            <h1>{project.title}</h1>
            {project.paused && (
              <Pill tone="needs" dot>
                Paused
              </Pill>
            )}
            {waiting > 0 && (
              <Pill tone="needs" dot>
                {waiting} need{waiting === 1 ? "s" : ""} you
              </Pill>
            )}
          </div>
          <div className="project-pause-control">
            <button
              className="btn btn-sm"
              aria-pressed={!!project.paused}
              disabled={busy}
              onClick={() =>
                run(async () => {
                  await setProjectPaused(project.id, !project.paused);
                  await refresh();
                })
              }
            >
              <Icon name={project.paused ? "Play" : "Pause"} size={14} />
              {project.paused ? "Resume project" : "Pause project"}
            </button>
            {project.paused && (
              <p className="soft small">
                Nothing new starts in this project. Steps already running
                finish.
              </p>
            )}
            <ErrorNotice error={error} />
          </div>
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
          {projectTabs.map((t) => (
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
        <TeamTab project={project} state={state} refresh={refresh} />
      )}
      {tab === "config" && (
        <ConfigTab
          project={project}
          members={state.members}
          refresh={refresh}
        />
      )}
      {tab === "activity" && <ActivityTab project={project} state={state} />}
      {route.request && (
        <RequestPanel
          key={route.request}
          project={project}
          task={request}
          state={state}
          seat={route.seat}
          onSeat={(seat) => {
            const task = route.request!;
            window.location.hash = seat
              ? seatHref(project.id, task, seat)
              : requestHref(project.id, task);
          }}
          refresh={refresh}
          onClose={() => {
            window.location.hash = projectHref(project.id);
          }}
        />
      )}
    </div>
  );
}
