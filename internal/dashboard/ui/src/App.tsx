import { ChatPanel } from "./ChatPanel";
import { NewProject } from "./ProjectForms";
import { Avatar, validTheme } from "./Identity";
import { PendingOperations } from "./PendingOperations";
import { DecisionHistory } from "./DecisionHistory";
import { DecisionCard } from "./DecisionCard";
import { Overview } from "./OverviewPage";
import { Projects } from "./ProjectsPage";
import { MemoryView } from "./MemoryPage";
import { Settings } from "./SettingsPage";
import { Login } from "./LoginScreen";
import { pages, type Page } from "./navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  bootstrapSession,
  errorText,
  normalizeState,
  pendingDecisions,
  type State,
} from "./api";
import { Empty, ErrorNotice, Icon, Mark, PageHeading } from "./ui";

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [page, setPage] = useState<Page>("Overview");
  const [selectedProject, setSelectedProject] = useState<string | null>(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [connectionError, setConnectionError] = useState("");
  const [newProject, setNewProject] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [chatExpanded, setChatExpanded] = useState(false);
  const [controlBusy, setControlBusy] = useState(false);
  const [controlError, setControlError] = useState("");
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
    document.title = state?.assistant.name
      ? `${page} · ${state.assistant.name}`
      : "Crew Assistant";
  }, [page, state?.assistant.name]);
  useEffect(() => {
    if (!chatOpen) return;
    const prior = document.activeElement as HTMLElement | null;
    conversation.current
      ?.querySelector<HTMLButtonElement>(".mobile-close")
      ?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        setChatOpen(false);
      }
      if (event.key !== "Tab") return;
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
  }, [chatOpen]);
  useEffect(() => {
    if (!window.matchMedia) return;
    const desktop = window.matchMedia("(min-width: 1001px)");
    const changed = () => {
      if (desktop.matches) setChatOpen(false);
      else setChatExpanded(false);
    };
    desktop.addEventListener("change", changed);
    return () => desktop.removeEventListener("change", changed);
  }, []);
  useEffect(() => {
    document.documentElement.dataset.theme = validTheme(state?.assistant.theme);
  }, [state?.assistant.theme]);
  useEffect(() => {
    const follow = () => {
      const page = pages.find(
        (p) => window.location.hash === `#/${p.toLowerCase()}`,
      );
      if (page) {
        setPage(page);
        setSelectedProject(null);
        return;
      }
      const match = /^#\/projects\/([^/]+)$/.exec(window.location.hash);
      if (match) {
        try {
          setSelectedProject(decodeURIComponent(match[1]));
          setPage("Projects");
          setChatExpanded(false);
          setChatOpen(false);
        } catch {
          /* Ignore malformed bookmarks. */
        }
      }
    };
    follow();
    window.addEventListener("hashchange", follow);
    return () => window.removeEventListener("hashchange", follow);
  }, []);
  function openProject(id: string) {
    window.location.hash = `/projects/${encodeURIComponent(id)}`;
    setSelectedProject(id);
    setPage("Projects");
    setChatExpanded(false);
    setChatOpen(false);
  }
  function navigate(next: Page) {
    // Each page has its own address, so it can be bookmarked or linked to.
    window.history.replaceState(
      window.history.state,
      "",
      `${window.location.pathname}${window.location.search}#/${next.toLowerCase()}`,
    );
    setChatExpanded(false);
    setPage(next);
    setSelectedProject(null);
  }
  async function togglePause() {
    if (!state) return;
    setControlBusy(true);
    setControlError("");
    try {
      await api("/api/control", {
        method: "POST",
        body: JSON.stringify({ paused: !state.paused }),
      });
      await refresh();
    } catch (error) {
      setControlError(errorText(error));
    } finally {
      setControlBusy(false);
    }
  }
  if (authRequired)
    return <Login onSuccess={refresh} initialError={connectionError} />;
  if (!state)
    return (
      <div className="loading-screen">
        <Mark />
        <h1>Crew Assistant</h1>
        <p role="status">
          {connectionError
            ? "The daemon is unavailable."
            : "Connecting to your assistant…"}
        </p>
        <ErrorNotice error={connectionError} />
        {connectionError && (
          <button className="button primary" onClick={() => void refresh()}>
            Try again
          </button>
        )}
      </div>
    );
  const decisions = pendingDecisions(state.decisions);
  const name = state.assistant.name || "Assistant";
  return (
    <div className={`app-shell ${chatExpanded ? "chat-expanded" : ""}`}>
      <a
        className="skip-link"
        href={chatExpanded ? "#chat-message" : "#main-content"}
      >
        Skip to content
      </a>
      <aside className="sidebar" inert={chatOpen}>
        <button
          className="brand"
          onClick={() => navigate("Overview")}
          aria-label={`${name} overview`}
        >
          <Avatar avatar={state.assistant.avatar} small />
          <span>
            {name}
            <small>PERSONAL ASSISTANT</small>
          </span>
        </button>
        <div className="sidebar-rule" />
        <nav aria-label="Main navigation">
          {pages.map((item) => (
            <button
              key={item}
              className={`nav-item ${page === item ? "active" : ""}`}
              aria-current={page === item ? "page" : undefined}
              onClick={() => navigate(item)}
            >
              <Icon name={item} />
              <span>{item}</span>
              {item === "Decisions" &&
                decisions.length + state.pending_operations.length > 0 && (
                  <span className="nav-count">
                    {decisions.length + state.pending_operations.length}
                  </span>
                )}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="private-label">
            <Icon name="Lock" size={14} /> Your private workspace
          </div>
          <div className="daemon-card">
            <span
              className={`status-dot ${connectionError ? "offline" : state.paused ? "paused" : ""}`}
            />
            <div>
              {connectionError
                ? "Connection interrupted"
                : state.paused
                  ? "Dispatch paused"
                  : "Daemon connected"}
              <small>
                {connectionError
                  ? "Showing last received state"
                  : state.paused
                    ? "Existing work may continue"
                    : "Coordination, with context"}
              </small>
            </div>
          </div>
          <button
            className="text-button pause-button"
            disabled={controlBusy || !!connectionError}
            onClick={() => void togglePause()}
          >
            <Icon name={state.paused ? "Play" : "Pause"} size={13} />
            {controlBusy
              ? "Updating…"
              : state.paused
                ? "Resume dispatch"
                : "Pause new work"}
          </button>
          <ErrorNotice error={controlError} />
        </div>
      </aside>
      <div className="workspace" inert={chatOpen || chatExpanded}>
        <header className="topbar">
          <div className="breadcrumbs">
            Workspace <span>/</span> <strong>{page}</strong>
          </div>
          <div className="topbar-actions">
            <span className="local-label">
              <span className="status-dot" />{" "}
              {state.demo ? "Demo workspace" : "Personal workspace"}
            </span>
            <button
              className="icon-button chat-toggle"
              aria-label="Open conversation"
              onClick={() => setChatOpen(true)}
            >
              <Icon name="Message" />
            </button>
            <button
              className="owner-avatar"
              aria-label="Open settings"
              onClick={() => navigate("Settings")}
            >
              You
            </button>
          </div>
        </header>
        {state.demo && (
          <div className="demo-banner">
            Preview mode · Sample projects. No live work is running.
          </div>
        )}
        {connectionError && (
          <div className="connection-banner" role="status">
            Connection interrupted. These are the last received updates.{" "}
            <button onClick={() => void refresh()}>Reconnect</button>
          </div>
        )}
        <main id="main-content" className="main-content">
          {page === "Overview" && (
            <Overview
              state={state}
              onNew={() => setNewProject(true)}
              onNavigate={navigate}
              onProject={openProject}
              refresh={refresh}
            />
          )}
          {page === "Projects" && (
            <Projects
              state={state}
              selected={selectedProject}
              onSelect={(id) => (id ? openProject(id) : navigate("Projects"))}
              onNew={() => setNewProject(true)}
              refresh={refresh}
            />
          )}
          {page === "Decisions" && (
            <section>
              <PageHeading
                eyebrow="YOUR JUDGMENT, WHERE IT COUNTS"
                title="Decisions"
                description="The context, the trade-off, and a recommendation. Make the call and the work can move."
              />
              <PendingOperations
                operations={state.pending_operations}
                projects={state.projects}
                refresh={refresh}
              />
              {decisions.length ? (
                <div className="decision-list">
                  {decisions.map((d) => (
                    <DecisionCard
                      key={d.id}
                      decision={d}
                      projects={state.projects}
                      refresh={refresh}
                    />
                  ))}
                </div>
              ) : !state.pending_operations.length ? (
                <Empty
                  icon="Check"
                  title="Nothing needs your decision"
                  action={
                    <button
                      className="button secondary"
                      onClick={() => navigate("Projects")}
                    >
                      View projects <Icon name="Arrow" size={15} />
                    </button>
                  }
                >
                  When your assistant needs your judgment, it will bring a clear
                  recommendation here.
                </Empty>
              ) : null}
              <DecisionHistory
                decisions={state.decisions.filter(
                  (d) => !decisions.includes(d),
                )}
                projects={state.projects}
              />
            </section>
          )}
          {page === "Memory" && <MemoryView state={state} refresh={refresh} />}
          {page === "Settings" && (
            <Settings
              state={state}
              refresh={refresh}
              control={
                <>
                  <button
                    className="button secondary"
                    disabled={controlBusy || !!connectionError}
                    onClick={() => void togglePause()}
                  >
                    <Icon name={state.paused ? "Play" : "Pause"} size={14} />
                    {controlBusy
                      ? "Updating…"
                      : state.paused
                        ? "Resume dispatch"
                        : "Pause new work"}
                  </button>
                  <ErrorNotice error={controlError} />
                </>
              }
            />
          )}
        </main>
      </div>
      <aside
        ref={conversation}
        role={chatOpen ? "dialog" : undefined}
        aria-modal={chatOpen || undefined}
        className={`conversation ${chatOpen ? "mobile-open" : ""}`}
        aria-label={`Conversation with ${name}`}
      >
        <ChatPanel
          onProjectOpen={openProject}
          state={state}
          refresh={refresh}
          onClose={() => setChatOpen(false)}
          expanded={chatExpanded}
          onExpand={() => {
            setChatExpanded(!chatExpanded);
            requestAnimationFrame(() =>
              document.getElementById("chat-message")?.focus(),
            );
          }}
        />
      </aside>
      {newProject && (
        <NewProject
          onClose={() => setNewProject(false)}
          onCreated={async () => {
            setNewProject(false);
            navigate("Projects");
            await refresh();
          }}
        />
      )}
    </div>
  );
}
