import { href, projectHref, type Route } from "./router";
import { projectGroup, projectTasks } from "./stages";
import { Avatar, hasFace } from "./Avatar";
import { ErrorNotice, Icon } from "./ui";
import { UsageStatus } from "./UsageStatus";
import type { State } from "./api";

const dotTone = {
  needs: "var(--needs)",
  working: "var(--work)",
  waiting: "var(--wait)",
  quiet: "var(--line-strong)",
};

const shownProjects = 8;

// daemonStatus is what the sidebar says about the daemon, most pressing
// first: unreachable, stopping, paused, then running.
function daemonStatus(state: State, offline: boolean) {
  if (offline) return { label: "Offline", color: "var(--block)", hint: "" };
  if (state.stopping)
    return {
      label: "Stopping",
      color: "var(--needs)",
      hint: "Finishing the steps already running, then crew-assistant stops. Nothing new starts.",
    };
  if (state.paused)
    return {
      label: "Paused",
      color: "var(--needs)",
      hint: "Nothing new starts. Steps already running finish.",
    };
  return { label: "Running", color: "var(--done)", hint: "" };
}

export function Sidebar({
  state,
  route,
  needs,
  offline,
  chatOpen,
  onChat,
  pausing,
  pauseError,
  onPause,
}: {
  state: State;
  route: Route;
  needs: number;
  offline: boolean;
  chatOpen: boolean;
  onChat: () => void;
  pausing: boolean;
  pauseError: string;
  onPause: () => void;
}) {
  const projects = state.projects.filter((p) => p.status !== "completed");
  const status = daemonStatus(state, offline);
  const current = (page: Route["page"]) =>
    route.page === page ||
    (page === "projects" && route.page === "project") ||
    (page === "team" && route.page === "member")
      ? "page"
      : undefined;
  return (
    <nav className="app-nav" aria-label="Main">
      <a className="brand" href={href({ page: "inbox" })}>
        {hasFace(state.assistant) ? (
          <Avatar of={state.assistant} size={26} />
        ) : (
          <span className="brand-mark" aria-hidden="true">
            C
          </span>
        )}
        <span>{state.assistant.name || "Assistant"}</span>
      </a>
      <div className="nav-group">
        <a
          className="nav-link"
          href={href({ page: "inbox" })}
          aria-current={current("inbox")}
        >
          <Icon name="Inbox" />
          <span className="nav-text">Inbox</span>
          {needs > 0 && (
            <span className="count tone-needs" aria-label={`${needs} need you`}>
              {needs}
            </span>
          )}
        </a>
        <a
          className="nav-link"
          href={href({ page: "projects" })}
          aria-current={current("projects")}
        >
          <Icon name="Projects" />
          <span className="nav-text">Projects</span>
        </a>
        <a
          className="nav-link"
          href={href({ page: "team" })}
          aria-current={current("team")}
        >
          <Icon name="Team" />
          <span className="nav-text">Team</span>
        </a>
        <a
          className="nav-link"
          href={href({ page: "memory" })}
          aria-current={current("memory")}
        >
          <Icon name="Memory" />
          <span className="nav-text">Memory</span>
        </a>
        <a
          className="nav-link"
          href={href({ page: "settings" })}
          aria-current={current("settings")}
        >
          <Icon name="Settings" />
          <span className="nav-text">Settings</span>
        </a>
        <button
          type="button"
          className="nav-link"
          aria-pressed={chatOpen}
          onClick={onChat}
        >
          <Icon name="Message" />
          <span className="nav-text">Chat</span>
          <span className="kbd nav-hint">⌘J</span>
        </button>
      </div>
      {projects.length > 0 && (
        <div className="nav-group nav-projects">
          <p className="label nav-label">Projects</p>
          {projects.slice(0, shownProjects).map((p) => (
            <a
              key={p.id}
              className="nav-link"
              href={projectHref(p.id)}
              aria-current={
                route.page === "project" && route.id === p.id
                  ? "page"
                  : undefined
              }
            >
              <span
                className="dot"
                style={{
                  color: dotTone[projectGroup(projectTasks(p, state.tasks))],
                }}
              />
              <span className="nav-text">{p.title}</span>
            </a>
          ))}
          {projects.length > shownProjects && (
            <a className="nav-link muted" href={href({ page: "projects" })}>
              <span className="nav-text">
                {projects.length - shownProjects} more
              </span>
            </a>
          )}
        </div>
      )}
      <div className="nav-status">
        <p className="nav-status-line">
          <span className="dot" style={{ color: status.color }} />
          {status.label}
        </p>
        {status.hint && <p className="hint">{status.hint}</p>}
        <UsageStatus />
        <button
          type="button"
          className="btn btn-sm"
          disabled={pausing || offline || state.stopping}
          onClick={onPause}
        >
          {state.paused ? "Resume teams" : "Pause all teams"}
        </button>
        <ErrorNotice error={pauseError} />
      </div>
    </nav>
  );
}
