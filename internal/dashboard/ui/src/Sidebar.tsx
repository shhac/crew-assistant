import { href, projectHref, type Route } from "./router";
import { projectGroup, projectTasks } from "./stages";
import { Avatar, ErrorNotice, Icon } from "./ui";
import type { State } from "./api";

const dotTone = {
  needs: "var(--needs)",
  working: "var(--work)",
  waiting: "var(--wait)",
  quiet: "var(--line-strong)",
};

const shownProjects = 8;

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
  const current = (page: Route["page"]) =>
    route.page === page ||
    (page === "projects" && route.page === "project") ||
    (page === "team" && route.page === "member")
      ? "page"
      : undefined;
  return (
    <nav className="app-nav" aria-label="Main">
      <a className="brand" href={href({ page: "inbox" })}>
        {state.assistant.avatar_svg ? (
          <Avatar svg={state.assistant.avatar_svg} size={26} />
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
          <span
            className="dot"
            style={{
              color: offline
                ? "var(--block)"
                : state.paused
                  ? "var(--needs)"
                  : "var(--done)",
            }}
          />
          {offline ? "Offline" : state.paused ? "Paused" : "Running"}
        </p>
        {state.paused && !offline && (
          <p className="hint">
            Nothing new starts. Steps already running finish.
          </p>
        )}
        <button
          type="button"
          className="btn btn-sm"
          disabled={pausing || offline}
          onClick={onPause}
        >
          {state.paused ? "Resume teams" : "Pause all teams"}
        </button>
        <ErrorNotice error={pauseError} />
      </div>
    </nav>
  );
}
