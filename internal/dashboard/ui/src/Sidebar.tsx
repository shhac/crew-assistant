import { href, projectHref, type Route } from "./router";
import { byTitle, projectGroup, projectTasks } from "./stages";
import { Avatar, hasFace } from "./Avatar";
import { ErrorNotice, Icon } from "./ui";
import { UsageStatus } from "./UsageStatus";
import { versionLabel, RollbackNotice } from "./UpdatesSettings";
import { span } from "./turns";
import { counted } from "./ui";
import type { State } from "./api";

const dotTone = {
  needs: "var(--needs)",
  working: "var(--work)",
  waiting: "var(--wait)",
  quiet: "var(--line-strong)",
};

const shownProjects = 8;

// daemonStatus is what the sidebar says about the daemon, most pressing
// first: unreachable, upgrade, stopping, paused, then running.
function daemonStatus(state: State, offline: boolean) {
  if (offline) return { label: "Offline", color: "var(--block)", hint: "" };
  const upgrade = state.upgrade;
  if (upgrade) {
    const target = versionLabel(upgrade.to);
    let hint = "";
    switch (upgrade.step) {
      case "draining": {
        const waiting = upgrade.waiting_on ?? [];
        const longest = Math.max(
          0,
          ...waiting.map((item) => elapsed(item.started_at)),
        );
        hint = waiting.length
          ? `Finishing ${counted(waiting.length, "step")}, longest ${Math.floor(longest / 60000)}m. Nothing new starts.`
          : "Finishing 0 steps. No running work remains.";
        break;
      }
      case "backing-up":
        hint = "Backing up";
        break;
      case "installing":
        hint = "Installing";
        break;
      case "handing-over":
        hint = `Starting ${target}`;
        break;
      case "probation":
        hint = `Checking ${target}`;
        break;
      case "rolling-back":
        return {
          label: "Restoring " + versionLabel(upgrade.from),
          color: "var(--needs)",
          hint: `Upgrade to ${target} failed. Restoring the previous version, state and config.`,
        };
      case "stopped-in-probation":
        return {
          label: "Upgrade interrupted",
          color: "var(--needs)",
          hint: `Stopped while checking ${target}. Health was not confirmed.`,
        };
      case "healthy":
      case "abandoned":
      case "backup-failed":
      case "install-failed":
      case "rolled-back":
        break;
    }
    if (hint)
      return { label: `Upgrading to ${target}`, color: "var(--needs)", hint };
  }
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

function elapsed(at: string) {
  const started = Date.parse(at);
  return Number.isFinite(started) && started > 0
    ? Math.max(0, Date.now() - started)
    : 0;
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
  const projects = state.projects
    .filter((p) => p.status !== "completed")
    .sort(byTitle);
  const status = daemonStatus(state, offline);
  const updateDecision = state.decisions.find(
    (d) =>
      d.kind === "upgrade-available" &&
      d.status === "open" &&
      state.update?.available === state.update?.decision_version,
  );
  const current = (page: Route["page"]) =>
    route.page === page ||
    (page === "projects" && route.page === "project") ||
    (page === "team" && (route.page === "member" || route.page === "assistant"))
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
        {!offline &&
          state.upgrade?.step === "draining" &&
          (state.upgrade.waiting_on ?? []).map((item, index) => (
            <p className="hint" key={`${item.kind}:${item.ref}:${index}`}>
              Waiting on {item.label || item.ref || item.kind} ({item.kind}) for{" "}
              {span(elapsed(item.started_at))}
            </p>
          ))}
        <RollbackNotice rollback={state.rollback} />
        {state.update?.running && (
          <p className="hint">{versionLabel(state.update.running)}</p>
        )}
        {state.update?.available &&
          state.update.available !== state.update.skipped && (
            <a
              className="hint"
              href={href(
                updateDecision
                  ? { page: "inbox", decision: updateDecision.id }
                  : { page: "settings", section: "updates" },
              )}
            >
              {versionLabel(state.update.available)} available
            </a>
          )}
        {state.update?.unavailable && (
          <p className="hint">{state.update.unavailable}</p>
        )}
        <UsageStatus pauses={state.engine_pauses} />
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
