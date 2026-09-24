import { useCallback, useEffect, useRef, useState } from "react";
import { ChatPanel } from "./ChatPanel";
import { NewProject } from "./ProjectForms";
import { InboxPage } from "./InboxPage";
import { ProjectsPage } from "./ProjectsPage";
import { ProjectPage } from "./ProjectPage";
import { MemoryView } from "./MemoryPage";
import { Settings } from "./SettingsPage";
import { Login } from "./LoginScreen";
import { Sidebar } from "./Sidebar";
import { applyAppearance } from "./appearance";
import { href, parseRoute, type Route } from "./router";
import {
  APIError,
  bootstrapSession,
  errorText,
  getState,
  normalizeState,
  pendingDecisions,
  setPaused,
  type State,
} from "./api";
import { avatarURL, ErrorNotice, useAction } from "./ui";
import { useChatPane } from "./chatPane";

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [route, setRoute] = useState<Route>(() =>
    parseRoute(window.location.hash),
  );
  const [authRequired, setAuthRequired] = useState(false);
  const [connectionError, setConnectionError] = useState("");
  const [newProject, setNewProject] = useState(false);
  const chat = useChatPane();
  const pause = useAction();
  const request = useRef(0);
  const refresh = useCallback(async () => {
    const generation = ++request.current;
    try {
      const value = await getState();
      if (generation !== request.current) return;
      setState(normalizeState(value));
      setAuthRequired(false);
      setConnectionError("");
    } catch (error) {
      if (generation !== request.current) return;
      if (error instanceof APIError && error.status === 401) {
        setAuthRequired(true);
        setState(null);
      } else setConnectionError(errorText(error));
    }
  }, []);
  useEffect(() => {
    void bootstrapSession()
      .then(refresh)
      .catch((error) => {
        setAuthRequired(true);
        setConnectionError(errorText(error));
      });
    const timer = setInterval(() => {
      if (!document.hidden) void refresh();
    }, 5000);
    return () => {
      clearInterval(timer);
      request.current++;
    };
  }, [refresh]);
  useEffect(() => {
    const follow = () => {
      const next = parseRoute(window.location.hash);
      setRoute(next);
      if (!/^#\/./.test(window.location.hash))
        window.history.replaceState(
          window.history.state,
          "",
          `${window.location.pathname}${window.location.search}${href(next)}`,
        );
    };
    follow();
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);
  useEffect(() => {
    if (state) applyAppearance(state.assistant.theme);
  }, [state?.assistant.theme]);
  const avatar = state?.assistant.avatar_svg;
  useEffect(() => {
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!avatar || !icon) return;
    icon.href = avatarURL(avatar);
  }, [avatar]);
  const needs = state
    ? pendingDecisions(state.decisions).length + state.pending_operations.length
    : 0;
  const project =
    route.page === "project"
      ? state?.projects.find((p) => p.id === route.id)
      : undefined;
  useEffect(() => {
    const titles: Record<Route["page"], string> = {
      inbox: "Inbox",
      projects: "Projects",
      project: project?.title ?? "Project",
      memory: "Memory",
      settings: "Settings",
    };
    const name = state?.assistant.name || "Assistant";
    document.title = `${needs ? `(${needs}) ` : ""}${titles[route.page]} · ${name}`;
  }, [route.page, project?.title, needs, state?.assistant.name]);
  async function togglePause() {
    if (!state) return;
    const paused = !state.paused;
    await pause.run(async () => {
      await setPaused(paused);
      await refresh();
    });
  }
  if (authRequired)
    return <Login onSuccess={refresh} initialError={connectionError} />;
  if (!state)
    return (
      <div className="boot">
        <span className="brand-mark" aria-hidden="true">
          C
        </span>
        <p role="status">
          {connectionError ? "Can't reach crew-assistant." : "Connecting…"}
        </p>
        <ErrorNotice error={connectionError} />
        {connectionError && (
          <button className="btn btn-primary" onClick={() => void refresh()}>
            Try again
          </button>
        )}
      </div>
    );
  const view = route.page === "project" ? `project/${route.id}` : route.page;
  return (
    <div
      className={`shell${chat.paneOpen ? " pane-open" : ""}${chat.expanded ? " chat-expanded" : ""}${chat.drawerOpen ? " drawer-open" : ""}`}
    >
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <Sidebar
        state={state}
        route={route}
        needs={needs}
        offline={!!connectionError}
        chatOpen={chat.shown}
        onChat={chat.toggle}
        pausing={pause.busy}
        pauseError={pause.error}
        onPause={() => void togglePause()}
      />
      <div className="workspace" inert={chat.drawerOpen || chat.expanded}>
        {state.demo && <p className="banner">Demo mode: no models run.</p>}
        {connectionError && (
          <p className="banner banner-alert" role="status">
            Can't reach crew-assistant. Showing what it last sent.{" "}
            <button className="link-button" onClick={() => void refresh()}>
              Retry
            </button>
          </p>
        )}
        <main id="main" tabIndex={-1}>
          {route.page === "inbox" && (
            <InboxPage
              state={state}
              refresh={refresh}
              onNew={() => setNewProject(true)}
            />
          )}
          {route.page === "projects" && (
            <ProjectsPage state={state} onNew={() => setNewProject(true)} />
          )}
          {route.page === "project" &&
            (project ? (
              <ProjectPage
                key={project.id}
                project={project}
                route={route}
                state={state}
                refresh={refresh}
              />
            ) : (
              <div className="page">
                <p className="muted">
                  This project isn't here any more.{" "}
                  <a href={href({ page: "projects" })}>See all projects</a>
                </p>
              </div>
            ))}
          {route.page === "memory" && (
            <MemoryView state={state} refresh={refresh} />
          )}
          {route.page === "settings" && (
            <Settings state={state} refresh={refresh} section={route.section} />
          )}
        </main>
      </div>
      {chat.shown && (
        <aside
          ref={chat.conversation}
          role={chat.drawerOpen ? "dialog" : undefined}
          aria-modal={chat.drawerOpen || undefined}
          className="chat-pane"
          aria-label="Chat"
        >
          <ChatPanel
            onProjectOpen={(id) => {
              window.location.hash = href({
                page: "project",
                id,
                tab: "board",
              });
            }}
            view={view}
            state={state}
            refresh={refresh}
            onClose={chat.close}
            expanded={chat.expanded}
            onExpand={chat.toggleExpanded}
          />
        </aside>
      )}
      {newProject && (
        <NewProject
          onClose={() => setNewProject(false)}
          onCreated={async (id) => {
            setNewProject(false);
            await refresh();
            window.location.hash = href({ page: "project", id, tab: "board" });
          }}
        />
      )}
    </div>
  );
}
