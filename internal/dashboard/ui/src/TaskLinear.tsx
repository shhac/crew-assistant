import { useEffect, useState } from "react";
import {
  api,
  getConfig,
  errorText,
  type Project,
  type LinearRef,
  type Task,
  linkTaskLinear,
  unlinkTaskLinear,
} from "./api";
import type { Connection } from "./ConnectionsSettings";
import { LinearSourceLink, type Option } from "./LinearSources";
import { ErrorNotice } from "./ui";

export function TaskLinear({
  project,
  task,
  refresh,
}: {
  project: Project;
  task: Task;
  refresh: () => Promise<void>;
}) {
  const [connections, setConnections] = useState<Connection[]>([]);
  const [connectionID, setConnectionID] = useState(
    project.linear?.connection_id ?? "",
  );
  const [profile, setProfile] = useState(project.linear?.profile ?? "");
  const [kind, setKind] = useState<"issue" | "project">("issue");
  const [ref, setRef] = useState("");
  const [projects, setProjects] = useState<Option[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    getConfig()
      .then((c) => {
        if (!live) return;
        const options = (c.connections ?? []).filter((c) => c.tool === "lin");
        setConnections(options);
        if (!project.linear && options.length === 1) {
          setConnectionID(options[0].id);
          if (options[0].profiles.length === 1)
            setProfile(options[0].profiles[0]);
        }
      })
      .catch((e) => {
        if (live) setError(errorText(e));
      });
    return () => {
      live = false;
    };
  }, [project.id]);
  useEffect(() => {
    let live = true;
    setProjects([]);
    if (kind === "project" && connectionID && profile)
      api<Option[]>(
        `/api/connections/${encodeURIComponent(connectionID)}/linear/projects?profile=${encodeURIComponent(profile)}`,
      )
        .then((rows) => {
          if (live) setProjects(rows);
        })
        .catch((e) => {
          if (live) setError(errorText(e));
        });
    return () => {
      live = false;
    };
  }, [kind, connectionID, profile]);
  const connection = connections.find((c) => c.id === connectionID);
  async function change(link?: LinearRef) {
    setBusy(true);
    setError("");
    try {
      if (link) await unlinkTaskLinear(project.id, task.id, link);
      else {
        await linkTaskLinear(project.id, task.id, {
          connection_id: connectionID,
          profile,
          kind,
          ref,
        });
        setRef("");
      }
      await refresh();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  if (
    !task.linear?.length &&
    !task.linear_links?.length &&
    !connections.length &&
    !error
  )
    return null;
  return (
    <section className="card">
      <h3>Linear</h3>
      <ErrorNotice error={error} />
      {(task.linear ?? []).map((l) => (
        <p key={l.id}>
          <LinearSourceLink link={l} />
          {" · Source issue · "}
          {l.by}
        </p>
      ))}
      {(task.linear_links ?? []).map((l) => (
        <p key={l.kind + l.id}>
          <a href={l.url} target="_blank" rel="noopener noreferrer">
            {l.kind === "project" ? l.title : `${l.identifier}: ${l.title}`}
          </a>
          {" · "}
          {l.by}{" "}
          <button
            className="btn"
            disabled={busy}
            onClick={() => void change(l)}
          >
            Remove
          </button>
        </p>
      ))}
      {!!connections.length && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void change();
          }}
        >
          {(connections.length > 1 || !connectionID) && (
            <label className="field">
              Connection
              <select
                value={connectionID}
                onChange={(e) => {
                  const c = connections.find((c) => c.id === e.target.value);
                  setConnectionID(e.target.value);
                  setProfile(c?.profiles.length === 1 ? c.profiles[0] : "");
                  setRef("");
                }}
              >
                <option value="">Choose a connection</option>
                {connections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          {((connection?.profiles.length ?? 0) > 1 || !profile) && (
            <label className="field">
              Account
              <select
                value={profile}
                onChange={(e) => {
                  setProfile(e.target.value);
                  setRef("");
                }}
              >
                <option value="">Choose an account</option>
                {connection?.profiles.map((p) => (
                  <option key={p}>{p}</option>
                ))}
              </select>
            </label>
          )}
          <label className="field">
            Link to
            <select
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as "issue" | "project");
                setRef("");
              }}
            >
              <option value="issue">Linear issue</option>
              <option value="project">Linear project</option>
            </select>
          </label>
          {kind === "issue" ? (
            <label className="field">
              Issue identifier or URL
              <input value={ref} onChange={(e) => setRef(e.target.value)} />
            </label>
          ) : (
            <label className="field">
              Project
              <select value={ref} onChange={(e) => setRef(e.target.value)}>
                <option value="">Choose a project</option>
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <button
            className="btn btn-primary"
            disabled={busy || !ref || !profile || !connectionID}
          >
            Add link
          </button>
        </form>
      )}
    </section>
  );
}
