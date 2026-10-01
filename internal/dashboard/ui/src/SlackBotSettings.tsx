import { useState } from "react";
import { Panel } from "./SettingsPanel";
import { section, type Config, type Integration, type Project } from "./api";
import { href } from "./router";
import { Pill, humanStatus } from "./ui";

export function SlackBotSettings({
  config,
  projects,
  integration,
  onChange,
}: {
  config: Config;
  projects: Project[];
  integration?: Integration;
  onChange: (value: Config) => void;
}) {
  const [editing, setEditing] = useState(false);
  const slack = section(config.slack);
  const value = (key: string) =>
    typeof slack[key] === "string" ? slack[key] : "";
  const projectID = value("project_id");
  const project = projects.find((p) => p.id === projectID);
  const hasPM = project?.playbook?.roles.some((r) => r.kinds.includes("pm"));
  const destination = projectID
    ? `${project?.title ?? "Unavailable project"} (Project manager)`
    : "Assistant";
  function set(key: string, text: string) {
    onChange({ ...config, slack: { ...slack, [key]: text } });
  }
  function field(
    key: string,
    label: string,
    options: { env?: boolean; placeholder?: string; hint?: string } = {},
  ) {
    return (
      <label htmlFor={`slack-${key}`}>
        <span id={`slack-${key}-label`}>{label}</span>
        <input
          id={`slack-${key}`}
          aria-labelledby={`slack-${key}-label`}
          aria-describedby={options.hint ? `slack-${key}-hint` : undefined}
          value={value(key)}
          autoComplete="off"
          placeholder={options.placeholder}
          pattern={options.env ? "[A-Za-z_][A-Za-z0-9_]*" : undefined}
          onChange={(e) => set(key, e.target.value)}
        />
        {options.hint && (
          <span className="hint" id={`slack-${key}-hint`}>
            {options.hint}
          </span>
        )}
      </label>
    );
  }
  return (
    <Panel title="Slack bot messaging">
      <div className="panel-head">
        <p className="soft">
          Send direct messages to your assistant or a project's manager in
          Slack.
        </p>
        <button
          type="button"
          className="btn btn-quiet btn-sm"
          aria-expanded={editing}
          aria-controls="slack-bot-editor"
          onClick={() => setEditing(!editing)}
        >
          {editing ? "Close" : "Edit"}
        </button>
      </div>
      <p>
        <strong>Destination:</strong> {destination}
      </p>
      {integration && (
        <p>
          <Pill
            tone={
              ["connected", "ready", "configured"].includes(integration.status)
                ? "done"
                : "needs"
            }
          >
            {integration.status === "restart_required"
              ? "Restart required"
              : humanStatus(integration.status)}
          </Pill>{" "}
          {integration.detail && (
            <span className="muted small">{integration.detail}</span>
          )}
        </p>
      )}
      {editing && (
        <div id="slack-bot-editor">
          <div className="form-row">
            {field("workspace_id", "Slack workspace ID", {
              hint: "The workspace where this bot is installed.",
            })}
            {field("owner_user_id", "Your Slack user ID")}
          </div>
          <label htmlFor="slack-project_id">
            <span id="slack-project_id-label">Destination</span>
            <select
              id="slack-project_id"
              aria-labelledby="slack-project_id-label"
              aria-describedby="slack-project_id-hint"
              value={projectID}
              onChange={(e) => set("project_id", e.target.value)}
            >
              <option value="">Assistant</option>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.title} (Project manager)
                </option>
              ))}
              {projectID && !project && (
                <option value={projectID}>Unavailable project</option>
              )}
            </select>
            <span className="hint" id="slack-project_id-hint">
              The assistant can coordinate all projects. A project manager works
              with only the selected project.
            </span>
          </label>
          {project && !hasPM && (
            <p className="hint">
              Choose a project manager in{" "}
              <a href={href({ page: "project", id: project.id, tab: "team" })}>
                {project.title}'s team
              </a>{" "}
              before messaging this project in Slack.
            </p>
          )}
          <div className="form-row">
            {field("bot_token_env", "Bot token variable", {
              env: true,
              placeholder: "SLACK_BOT_TOKEN",
              hint: "The environment variable's name, never the token.",
            })}
            {field("app_token_env", "App token variable", {
              env: true,
              placeholder: "SLACK_APP_TOKEN",
              hint: "The environment variable's name, never the token.",
            })}
          </div>
          <p className="hint">
            Restart crew-assistant after changing this connection. The current
            connection stays in use until restart.
          </p>
        </div>
      )}
    </Panel>
  );
}
