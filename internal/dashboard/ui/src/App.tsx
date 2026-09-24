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
  api,
  APIError,
  bootstrapSession,
  errorText,
  normalizeState,
  pendingDecisions,
  type State,
} from "./api";
import { ErrorNotice } from "./ui";

const chatKey = "crew-assistant.chat";

function rememberedChat() {
  try {
    return localStorage.getItem(chatKey) !== "closed";
  } catch {
    return true;
  }
}

const narrow = () =>
  typeof window.matchMedia === "function" &&
  window.matchMedia("(max-width: 1000px)").matches;

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [route, setRoute] = useState<Route>(() =>
    parseRoute(window.location.hash),
  );
  const [authRequired, setAuthRequired] = useState(false);
  const [connectionError, setConnectionError] = useState("");
  const [newProject, setNewProject] = useState(false);
  // On a wide screen the conversation is a pane beside the work; on a narrow
  // one it is a drawer over it, closed until asked for.
  const [paneOpen, setPaneOpen] = useState(rememberedChat);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [chatExpanded, setChatExpanded] = useState(false);
  const [pausing, setPausing] = useState(false);
  const [pauseError, setPauseError] = useState("");
  const request = useRef(0);
  const conversation = useRef<HTMLElement>(null);
  const refresh = useCallback(async () => {
    const generation = ++request.current;
    try {
      const value = await api<State>("/api/state");
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
      setChatExpanded(false);
      setDrawerOpen(false);
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
  const toggleChat = useCallback(() => {
    if (narrow()) {
      setDrawerOpen((open) => !open);
      return;
    }
    setPaneOpen((open) => {
      try {
        localStorage.setItem(chatKey, open ? "closed" : "open");
      } catch {
        // The choice still holds for this visit.
      }
      if (open) setChatExpanded(false);
      return !open;
    });
  }, []);
  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "j") {
        event.preventDefault();
        toggleChat();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => document.removeEventListener("keydown", keydown);
  }, [toggleChat]);
  useEffect(() => {
    if (!drawerOpen) return;
    const prior = document.activeElement as HTMLElement | null;
    conversation.current
      ?.querySelector<HTMLButtonElement>(".mobile-close")
      ?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setDrawerOpen(false);
      }
      // A Tab the conversation already handled (accepting a suggestion) is not
      // focus movement.
      if (event.key !== "Tab" || event.defaultPrevented) return;
      const focusable = Array.from(
        conversation.current?.querySelectorAll<HTMLElement>(
          "button:not([disabled]),textarea:not([disabled]),input:not([disabled]),a[href]",
        ) || [],
      ).filter((el) => el.getClientRects().length > 0);
      const first = focusable[0],
        last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      prior?.focus();
    };
  }, [drawerOpen]);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const wide = window.matchMedia("(min-width: 1001px)");
    const changed = () => {
      if (wide.matches) setDrawerOpen(false);
      else setChatExpanded(false);
    };
    wide.addEventListener("change", changed);
    return () => wide.removeEventListener("change", changed);
  }, []);
  async function togglePause() {
    if (!state) return;
    setPausing(true);
    setPauseError("");
    try {
      await api("/api/control", {
        method: "POST",
        body: JSON.stringify({ paused: !state.paused }),
      });
      await refresh();
    } catch (error) {
      setPauseError(errorText(error));
    } finally {
      setPausing(false);
    }
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
  const chatShown = paneOpen || drawerOpen;
  const view = route.page === "project" ? `project/${route.id}` : route.page;
  return (
    <div
      className={`shell${paneOpen ? " pane-open" : ""}${chatExpanded ? " chat-expanded" : ""}${drawerOpen ? " drawer-open" : ""}`}
    >
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <Sidebar
        state={state}
        route={route}
        needs={needs}
        offline={!!connectionError}
        chatOpen={chatShown}
        onChat={toggleChat}
        pausing={pausing}
        pauseError={pauseError}
        onPause={() => void togglePause()}
      />
      <div className="workspace" inert={drawerOpen || chatExpanded}>
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
      {chatShown && (
        <aside
          ref={conversation}
          role={drawerOpen ? "dialog" : undefined}
          aria-modal={drawerOpen || undefined}
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
            onClose={() => (drawerOpen ? setDrawerOpen(false) : toggleChat())}
            expanded={chatExpanded}
            onExpand={() => {
              setChatExpanded(!chatExpanded);
              requestAnimationFrame(() =>
                document.getElementById("chat-message")?.focus(),
              );
            }}
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
