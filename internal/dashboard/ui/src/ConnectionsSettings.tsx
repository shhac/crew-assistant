import { useEffect, useState } from "react";
import { api, errorText } from "./api";
export interface Connection {
  id: string;
  name: string;
  tool: "lin" | "agent-slack" | "agent-notion" | "agent-fathom";
  profiles: string[];
  import_assignments?: boolean;
}
interface ProfileDiscovery {
  tool: string;
  profiles: { name: string; detail?: string }[];
  available: boolean;
  detail: string;
  selectable: boolean;
}
const tools: { id: Connection["tool"]; name: string; description: string }[] = [
  { id: "lin", name: "Linear", description: "Assignments and project context" },
  {
    id: "agent-slack",
    name: "Slack",
    description: "Conversations and team context",
  },
  {
    id: "agent-notion",
    name: "Notion",
    description: "Documents and shared knowledge",
  },
  {
    id: "agent-fathom",
    name: "Fathom",
    description: "Meeting notes and decisions",
  },
];
export function ConnectionsSettings({
  connections,
  onChange,
}: {
  connections: Connection[];
  onChange: (connections: Connection[]) => void;
}) {
  return (
    <section
      className="tab-panel card settings-panel"
      aria-labelledby="connections-title"
    >
      <h2 id="connections-title">Connections</h2>
      <p className="soft">
        Optional. They let the assistant read from services you use. Keep work
        and personal accounts separate.
      </p>
      {!connections.length && <p className="muted">No connections.</p>}
      {connections.map((connection, index) => (
        <ConnectionEditor
          key={connection.id}
          connection={{ ...connection, profiles: connection.profiles ?? [] }}
          index={index}
          onChange={(next) =>
            onChange(
              connections.map((value, i) => (i === index ? next : value)),
            )
          }
          onRemove={() => onChange(connections.filter((_, i) => i !== index))}
        />
      ))}
      <div className="actions">
        <button
          type="button"
          className="btn"
          onClick={() =>
            onChange([
              ...connections,
              {
                id: `connection-${Math.random().toString(36).slice(2, 10)}`,
                name: "",
                tool: "lin",
                profiles: [],
                import_assignments: false,
              },
            ])
          }
        >
          Add a connection
        </button>
      </div>
      <p className="hint">
        Each signs in through that service's own CLI, so no passwords go in
        here. Linear, Slack and Fathom need at least one account chosen; Notion
        uses the one its CLI has.
      </p>
    </section>
  );
}
function ConnectionEditor({
  connection,
  index,
  onChange,
  onRemove,
}: {
  connection: Connection;
  index: number;
  onChange: (connection: Connection) => void;
  onRemove: () => void;
}) {
  const [discovery, setDiscovery] = useState<ProfileDiscovery | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    setDiscovery(null);
    api<ProfileDiscovery>(
      `/api/connection-profiles?tool=${encodeURIComponent(connection.tool)}`,
    )
      .then((value) => {
        if (current) setDiscovery(value);
      })
      .catch((e) => {
        if (current) setError(errorText(e));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [connection.tool, revision]);
  const usesDefaultAccount = connection.tool === "agent-notion";
  const service = tools.find((t) => t.id === connection.tool);
  const discovered = (discovery?.profiles || []).map((p) => p.name);
  const names = [...new Set([...discovered, ...connection.profiles])];
  return (
    <fieldset className="connection card">
      <legend className="sr-only">
        {connection.name || `New ${service?.name || "connection"} connection`}
      </legend>
      <div className="panel-head">
        <p>
          <strong>
            {connection.name || `New ${service?.name ?? ""} connection`}
          </strong>{" "}
          <span className="muted small">{service?.description}</span>
        </p>
        <button
          type="button"
          className="btn btn-quiet btn-sm btn-danger"
          onClick={onRemove}
          aria-label={`Remove connection ${connection.name || index + 1}`}
        >
          Remove
        </button>
      </div>
      <div className="form-row">
        <label htmlFor={`connection-${index}-name`}>
          Name
          <input
            id={`connection-${index}-name`}
            value={connection.name}
            onChange={(e) => onChange({ ...connection, name: e.target.value })}
            placeholder="Work"
            required
            maxLength={80}
          />
        </label>
        <label htmlFor={`connection-${index}-tool`}>
          Service
          <select
            id={`connection-${index}-tool`}
            value={connection.tool}
            onChange={(e) =>
              onChange({
                ...connection,
                tool: e.target.value as Connection["tool"],
                profiles: [],
                import_assignments: false,
              })
            }
          >
            {tools.map((tool) => (
              <option key={tool.id} value={tool.id}>
                {tool.name}
              </option>
            ))}
          </select>
        </label>
      </div>
      {connection.tool === "lin" && (
        <>
          <label className="check">
            <input
              type="checkbox"
              checked={connection.import_assignments ?? false}
              onChange={(e) =>
                onChange({
                  ...connection,
                  import_assignments: e.target.checked,
                })
              }
              aria-describedby={`connection-${index}-import-hint`}
            />
            <span>Add issues assigned to you as projects</span>
          </label>
          <p className="hint" id={`connection-${index}-import-hint`}>
            Leave off to use Linear only for reading.
          </p>
        </>
      )}
      <div className="panel-head">
        <span className="label">
          {usesDefaultAccount
            ? "Uses the account its CLI has"
            : connection.tool === "agent-slack"
              ? "Workspaces it may use"
              : "Accounts it may use"}
        </span>
        <button
          type="button"
          className="btn btn-quiet btn-sm"
          disabled={loading}
          onClick={() => setRevision(revision + 1)}
        >
          {loading ? "Looking…" : "Refresh"}
        </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {discovery?.detail && <p className="hint">{discovery.detail}</p>}
      {!usesDefaultAccount && !loading && !names.length && (
        <p className="hint">
          None found. Sign in with the {service?.name} CLI (
          <code>{connection.tool}</code>), then refresh.
        </p>
      )}
      {!usesDefaultAccount && (
        <div className="choices">
          {names.map((name) => (
            <label key={name} className="check">
              <input
                type="checkbox"
                checked={connection.profiles.includes(name)}
                disabled={
                  loading ||
                  !discovery?.available ||
                  !discovery.selectable ||
                  !discovered.includes(name)
                }
                onChange={(e) =>
                  onChange({
                    ...connection,
                    profiles: e.target.checked
                      ? [...connection.profiles, name]
                      : connection.profiles.filter((p) => p !== name),
                  })
                }
              />
              <span>
                {name}
                {discovery?.profiles.find((p) => p.name === name)?.detail && (
                  <small>
                    {discovery.profiles.find((p) => p.name === name)?.detail}
                  </small>
                )}
                {!discovered.includes(name) && !loading && (
                  <small>Saved, but not found now</small>
                )}
              </span>
            </label>
          ))}
        </div>
      )}
      {usesDefaultAccount && connection.profiles.length > 0 && (
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => onChange({ ...connection, profiles: [] })}
        >
          Use the CLI's account
        </button>
      )}
    </fieldset>
  );
}
