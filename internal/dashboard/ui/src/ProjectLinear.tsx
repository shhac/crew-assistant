import { useEffect, useRef, useState } from "react";
import {
  api,
  getConfig,
  errorText,
  type Project,
  type LinearLink,
} from "./api";
import type { Connection } from "./ConnectionsSettings";
import type { Option } from "./LinearSources";
import { ErrorNotice } from "./ui";

const empty: LinearLink = {
  connection_id: "",
  profile: "",
  kind: "team",
  id: "",
  name: "",
  rules: { pick_up: false, states: [], assignee: "unassigned", users: [] },
};
export function linearRuleText(link: LinearLink) {
  const who =
    link.rules.assignee === "unassigned"
      ? "unassigned issues"
      : link.rules.assignee === "me"
        ? "issues assigned to me"
        : link.rules.assignee === "users"
          ? `issues assigned to ${link.rules.users.map((u) => u.name).join(", ")}`
          : "issues assigned to anyone";
  return `${link.rules.pick_up ? "Pick up" : "Pick-up is off for"} ${who}${link.rules.states.length ? ` in ${link.rules.states.join(", ")}` : " in any status"}.`;
}
export function ProjectLinear({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<LinearLink>(project.linear ?? empty);
  const [teams, setTeams] = useState<Option[]>([]);
  const [projects, setProjects] = useState<Option[]>([]);
  const targets = draft.kind === "team" ? teams : projects;
  const optionCache = useRef(new Map<string, Promise<Option[]>>());
  const [states, setStates] = useState<Option[]>([]);
  const [users, setUsers] = useState<Option[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    getConfig()
      .then((c) => {
        if (live)
          setConnections((c.connections ?? []).filter((c) => c.tool === "lin"));
      })
      .catch((e) => {
        if (live) setError(errorText(e));
      });
    return () => {
      live = false;
    };
  }, []);
  function readOptions(kind: string, resource = "") {
    const base = `/api/connections/${encodeURIComponent(draft.connection_id)}/linear/${kind}?profile=${encodeURIComponent(draft.profile)}`;
    const url =
      base +
      `&${kind === "project-teams" ? "project" : "team"}=${encodeURIComponent(resource)}`;
    let pending = optionCache.current.get(url);
    if (!pending) {
      pending = api<Option[]>(url);
      optionCache.current.set(url, pending);
      pending.catch(() => optionCache.current.delete(url));
    }
    return pending;
  }
  useEffect(() => {
    if (!editing || !draft.connection_id || !draft.profile) return;
    let live = true;
    setTeams([]);
    setProjects([]);
    setUsers([]);
    setError("");
    Promise.all([
      readOptions("teams"),
      readOptions("projects"),
      readOptions("users"),
    ])
      .then(([boards, options, people]) => {
        if (live) {
          setTeams(boards);
          setProjects(options);
          setUsers(people);
        }
      })
      .catch((e) => {
        if (live) setError(errorText(e));
      });
    return () => {
      live = false;
    };
  }, [editing, draft.connection_id, draft.profile]);
  useEffect(() => {
    setStates([]);
    if (!editing || !draft.connection_id || !draft.profile || !draft.id) return;
    let live = true;
    async function loadStates() {
      const selected =
        draft.kind === "team"
          ? [{ id: draft.id, name: draft.name }]
          : await readOptions("project-teams", draft.id);
      const allStates: Option[] = [];
      // Keep CLI reads bounded in flight even when a project spans many teams.
      for (const team of selected) {
        if (!live) return;
        allStates.push(...(await readOptions("states", team.id)));
      }
      if (live)
        setStates([...new Map(allStates.map((s) => [s.name, s])).values()]);
    }
    loadStates().catch((e) => {
      if (live) setError(errorText(e));
    });
    return () => {
      live = false;
    };
  }, [editing, draft.connection_id, draft.profile, draft.kind, draft.id]);
  async function save(unlink = false) {
    setBusy(true);
    setError("");
    try {
      await api(`/api/projects/${project.id}/linear`, {
        method: unlink ? "DELETE" : "PUT",
        ...(unlink ? {} : { body: JSON.stringify(draft) }),
      });
      await refresh();
      setEditing(false);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const connection = connections.find((c) => c.id === draft.connection_id);
  return (
    <section className="card" aria-labelledby="linear-title">
      <div className="card-head">
        <h3 id="linear-title">Linear</h3>
        {!editing && (
          <button
            className="btn"
            onClick={() => {
              setDraft(project.linear ?? empty);
              setEditing(true);
            }}
          >
            Edit
          </button>
        )}
      </div>
      <ErrorNotice error={error} />
      {!editing ? (
        <>
          {project.linear ? (
            <>
              <p>
                {project.linear.name} · {project.linear.profile}
              </p>
              <p>{linearRuleText(project.linear)}</p>
              {project.linear.last_error && (
                <ErrorNotice error={project.linear.last_error} />
              )}
              <button
                className="btn"
                disabled={busy}
                onClick={() => void save(true)}
              >
                Unlink
              </button>
            </>
          ) : (
            <p className="muted">
              No Linear link. Linking is optional for each project.
            </p>
          )}
        </>
      ) : (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          {!connections.length && (
            <p>Add a Linear connection in Settings → Connections.</p>
          )}
          <label className="field">
            Connection
            <select
              value={draft.connection_id}
              onChange={(e) =>
                setDraft({ ...empty, connection_id: e.target.value })
              }
            >
              <option value="">Choose a connection</option>
              {connections.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            Account
            <select
              value={draft.profile}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  profile: e.target.value,
                  id: "",
                  name: "",
                  rules: empty.rules,
                })
              }
            >
              <option value="">Choose an account</option>
              {connection?.profiles.map((p) => (
                <option key={p}>{p}</option>
              ))}
            </select>
          </label>
          <label className="field">
            Link to
            <select
              value={draft.kind}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  kind: e.target.value as "team" | "project",
                  id: "",
                  name: "",
                  rules: { ...draft.rules, states: [] },
                })
              }
            >
              <option value="team">Linear team (board)</option>
              <option value="project">Linear project</option>
            </select>
          </label>
          <label className="field">
            {draft.kind === "team" ? "Team" : "Project"}
            <select
              value={draft.id}
              onChange={(e) => {
                const o = targets.find((t) => t.id === e.target.value);
                setDraft({
                  ...draft,
                  id: o?.id ?? "",
                  name: o?.name ?? "",
                  rules: { ...draft.rules, states: [] },
                });
              }}
            >
              <option value="">Choose</option>
              {targets.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            Statuses (none means any)
            <select
              multiple
              value={draft.rules.states}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  rules: {
                    ...draft.rules,
                    states: Array.from(
                      e.target.selectedOptions,
                      (o) => o.value,
                    ),
                  },
                })
              }
            >
              {states.map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            Assigned to
            <select
              value={draft.rules.assignee}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  rules: {
                    ...draft.rules,
                    assignee: e.target.value as LinearLink["rules"]["assignee"],
                    users: [],
                  },
                })
              }
            >
              <option value="any">Anyone</option>
              <option value="unassigned">Unassigned</option>
              <option value="me">Me (the selected account)</option>
              <option value="users">Chosen users</option>
            </select>
          </label>
          {draft.rules.assignee === "users" && (
            <label className="field">
              Users
              <select
                multiple
                value={draft.rules.users.map((u) => u.id)}
                onChange={(e) => {
                  const ids = Array.from(
                    e.target.selectedOptions,
                    (o) => o.value,
                  );
                  setDraft({
                    ...draft,
                    rules: {
                      ...draft.rules,
                      users: users.filter((u) => ids.includes(u.id)),
                    },
                  });
                }}
              >
                {users.map((u) => (
                  <option key={u.id} value={u.id}>
                    {u.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label className="field">
            <input
              type="checkbox"
              checked={draft.rules.pick_up}
              onChange={(e) =>
                setDraft({
                  ...draft,
                  rules: { ...draft.rules, pick_up: e.target.checked },
                })
              }
            />
            Pick up matching issues every five minutes
          </label>
          <div className="actions">
            <button className="btn btn-primary" disabled={busy || !draft.id}>
              Save
            </button>
            <button
              type="button"
              className="btn"
              onClick={() => setEditing(false)}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
