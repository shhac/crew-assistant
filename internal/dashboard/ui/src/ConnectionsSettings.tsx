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
      className="connections-settings"
      aria-labelledby="connections-title"
    >
      <div className="settings-section-title">
        <span className="connection-heading-symbol" aria-hidden="true">
          ↗
        </span>
        <div>
          <h2 id="connections-title">Your connected accounts</h2>
          <p>
            Connections are optional resources for your projects. Keep work and
            personal accounts distinct, and choose what your assistant can read.
          </p>
        </div>
      </div>
      {!connections.length && (
        <div className="connections-empty">
          <p>Your projects live in crew-assistant.</p>
          <span>
            Add existing folders or create projects without connecting any
            service. Personal projects need no Linear workspace. Connect Linear,
            Slack, Notion or Fathom when their context is useful.
          </span>
        </div>
      )}
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
      <button
        type="button"
        className="button secondary"
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
      <p className="field-hint">
        Accounts authenticate through their CLI. No credentials are entered
        here. Choose at least one profile for Linear, Slack or Fathom. Notion
        uses the account already selected in its CLI; no profile is needed.
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
    <fieldset className="connection-editor">
      <legend>
        {connection.name || `New ${service?.name || "connection"} connection`}
      </legend>
      <div className="connection-editor-heading">
        <span className="integration-symbol" aria-hidden="true">
          {service?.name.slice(0, 1)}
        </span>
        <p>{service?.description}</p>
        <button
          type="button"
          className="text-button"
          onClick={onRemove}
          aria-label={`Remove connection ${connection.name || index + 1}`}
        >
          Remove
        </button>
      </div>
      <div className="connection-name-grid">
        <label htmlFor={`connection-${index}-name`}>
          Connection name
          <input
            id={`connection-${index}-name`}
            value={connection.name}
            onChange={(e) => onChange({ ...connection, name: e.target.value })}
            placeholder="e.g. Work projects"
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
          <label className="profile-choice">
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
            <span>Import assigned issues as projects</span>
          </label>
          <p className="field-hint" id={`connection-${index}-import-hint`}>
            Optional. Automatically add issues assigned to you from the selected
            profiles. Leave off to use Linear only as context. Connecting a work
            workspace does not require tracking personal projects there.
          </p>
        </>
      )}
      <div className="profile-selection-heading">
        <strong>
          {usesDefaultAccount
            ? "CLI default account"
            : connection.tool === "agent-slack"
              ? "Workspace aliases"
              : "Allowed account profiles"}
        </strong>
        <button
          type="button"
          className="text-button"
          disabled={loading}
          onClick={() => setRevision(revision + 1)}
        >
          {loading ? "Finding profiles…" : "Refresh profiles"}
        </button>
      </div>
      {error && (
        <p className="error-notice" role="alert">
          {error}
        </p>
      )}
      {discovery?.detail && <p className="field-hint">{discovery.detail}</p>}
      {!usesDefaultAccount && !loading && !names.length && (
        <p className="field-hint">
          No profiles found. Set up an account with{" "}
          <code>{connection.tool}</code>, then refresh.
        </p>
      )}
      {!usesDefaultAccount && (
        <div className="profile-choices">
          {names.map((name) => (
            <label key={name} className="profile-choice">
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
                  <small>Saved profile; currently unavailable</small>
                )}
              </span>
            </label>
          ))}
        </div>
      )}
      {usesDefaultAccount && connection.profiles.length > 0 && (
        <button
          type="button"
          className="button secondary"
          onClick={() => onChange({ ...connection, profiles: [] })}
        >
          Use CLI default account
        </button>
      )}
    </fieldset>
  );
}
