import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { AssistantSetup } from "./AssistantSetup";
import { ConnectionsSettings } from "./ConnectionsSettings";
import { ChatSettings } from "./ChatSettings";
import { ModelSettings } from "./ModelSettings";
import { WorkerUsageSettings } from "./WorkerUsageSettings";
import { WorkerSettings } from "./WorkerSettings";
import { ThemePicker } from "./Identity";
import {
  ErrorNotice,
  humanStatus,
  Icon,
  Mark,
  PageHeading,
  Status,
} from "./ui";
import {
  api,
  errorText,
  type Config,
  type ModelProfile,
  type Project,
  type State,
} from "./api";

export function Settings({
  state,
  refresh,
  control,
}: {
  state: State;
  refresh: () => Promise<void>;
  control: ReactNode;
}) {
  const [config, setConfig] = useState<Config | null>(null);
  const [name, setName] = useState(state.assistant.name);
  const [personality, setPersonality] = useState(state.assistant.personality);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let alive = true;
    api<Config>("/api/config")
      .then((value) => {
        if (alive) setConfig(value);
      })
      .catch((e) => {
        if (alive) setError(errorText(e));
      });
    return () => {
      alive = false;
    };
  }, []);
  async function save(e: FormEvent) {
    e.preventDefault();
    if (!config) return;
    setBusy(true);
    setError("");
    setSaved(false);
    try {
      const next = {
        ...config,
        assistant: { ...config.assistant, name: name.trim(), personality },
      };
      await api("/api/config", { method: "PUT", body: JSON.stringify(next) });
      setConfig(next);
      setSaved(true);
      await refresh();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      <PageHeading
        eyebrow="MAKE IT YOURS"
        title="Settings"
        description="A familiar voice, a clear remit, and connections you control."
      />
      <ErrorNotice error={error} />
      <AssistantSetup
        currentName={state.assistant.name}
        demo={state.demo}
        onApplied={async (assistant) => {
          setName(assistant.name || state.assistant.name);
          setPersonality(assistant.personality || "");
          setConfig(await api<Config>("/api/config"));
          await refresh();
        }}
      />
      <form className="settings-form" onSubmit={save}>
        <div className="settings-section-title">
          <Mark small />
          <div>
            <h2>Your assistant</h2>
            <p>Choose the name and character you want to work with.</p>
          </div>
        </div>
        <label htmlFor="assistant-name">
          Name
          <input
            id="assistant-name"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setSaved(false);
            }}
            maxLength={80}
            required
          />
        </label>
        <label htmlFor="personality">
          Personality
          <textarea
            id="personality"
            value={personality}
            onChange={(e) => {
              setPersonality(e.target.value);
              setSaved(false);
            }}
            rows={4}
            maxLength={10000}
            placeholder="Calm, direct, curious. Bring a recommendation, not just a question."
          />
        </label>
        <p className="field-hint">
          Personality changes how your assistant communicates. Authority is
          configured separately.
        </p>
        {config && (
          <ThemePicker
            value={config.assistant?.theme}
            onChange={(theme) => {
              setConfig({
                ...config,
                assistant: { ...config.assistant, theme },
              });
              setSaved(false);
            }}
          />
        )}
        {config && (
          <ConnectionsSettings
            connections={config.connections || []}
            onChange={(connections) => {
              setConfig({ ...config, connections });
              setSaved(false);
            }}
          />
        )}
        {config && (
          <ChatSettings
            config={config}
            onChange={(next) => {
              setConfig(next);
              setSaved(false);
            }}
          />
        )}
        {config && (
          <ConfigurationFields
            projects={state.projects}
            config={config}
            onChange={(next) => {
              setConfig(next);
              setSaved(false);
            }}
          />
        )}
        {config && (
          <WorkerSettings
            projects={state.projects}
            workers={config.workers || []}
            onChange={(workers) => {
              setConfig({ ...config, workers });
              setSaved(false);
            }}
          />
        )}
        <div className="settings-save">
          <span role="status">{saved ? "Preferences saved." : ""}</span>
          <button
            className="button primary"
            disabled={busy || !config || !name.trim()}
          >
            {busy ? "Saving…" : "Save preferences"}
          </button>
        </div>
      </form>
      <section className="section-block">
        <div className="section-heading">
          <h2>Connection status</h2>
        </div>
        <p className="section-description">
          Connection credentials stay outside the dashboard. Configure
          credential references through the CLI.
        </p>
        <div className="integrations">
          {state.integrations.length ? (
            state.integrations.map((i) => (
              <div className="integration-row" key={i.id}>
                <span className="integration-symbol">{i.name.slice(0, 1)}</span>
                <div>
                  <strong>{i.name}</strong>
                  <p>{i.detail || "No additional connection details"}</p>
                </div>
                <Status
                  tone={
                    ["connected", "ready", "configured"].includes(i.status)
                      ? "green"
                      : "amber"
                  }
                >
                  {humanStatus(i.status)}
                </Status>
              </div>
            ))
          ) : (
            <div className="integration-empty">
              <Icon name="Settings" />
              <p>
                No connections configured. Run{" "}
                <code>crew-assistant doctor</code> to check setup.
              </p>
            </div>
          )}
        </div>
      </section>
      <section className="section-block">
        <div className="section-heading">
          <h2>Dispatch control</h2>
        </div>
        <p className="section-description">
          Pausing stops new work from being dispatched. Existing agent runs may
          continue.
        </p>
        {control}
      </section>
      <section className="settings-boundaries">
        <Icon name="Lock" size={20} />
        <div>
          <h3>Built-in boundaries</h3>
          <p>
            The assistant coordinates approved agents. It cannot write project
            code, deploy, access production data, or buy things. A personality
            change cannot override these boundaries.
          </p>
        </div>
      </section>
    </section>
  );
}

/**
 * The global setting is a default for workers created later; a project worker
 * can carry its own model. Showing both prevents the settings page and a
 * project page from looking as though they disagree.
 */
function WorkerModelOverrides({
  config,
  projects,
}: {
  config: Config;
  projects: Project[];
}) {
  const fallback = (config.worker_model || {}) as ModelProfile;
  const overridden = (config.workers || []).filter(
    (worker) => worker.managed === true && !!worker.model_profile,
  );
  if (!overridden.length) return null;
  const describe = (model: ModelProfile) =>
    [model.engine, model.model, model.effort && `${model.effort} effort`]
      .filter(Boolean)
      .join(" · ");
  return (
    <div className="worker-overrides">
      <h3>Project workers with their own model</h3>
      <p className="field-hint">
        These projects do not use the default above. The effective model is what
        their next assignment will run.
      </p>
      <dl>
        {overridden.map((worker) => (
          <div key={worker.id}>
            <dt>
              {projects.find((p) => p.id === worker.project_id)?.title ||
                worker.name ||
                worker.id}
            </dt>
            <dd>{describe(worker.model_profile || {}) || "Not recorded"}</dd>
          </div>
        ))}
        <div>
          <dt>Default for new workers</dt>
          <dd>{describe(fallback) || "Not recorded"}</dd>
        </div>
      </dl>
    </div>
  );
}

function ConfigurationFields({
  config,
  onChange,
  projects,
}: {
  config: Config;
  onChange: (value: Config) => void;
  projects: Project[];
}) {
  const [listDrafts, setListDrafts] = useState<Record<string, string>>({});
  const linear = (config.linear || {}) as Record<string, unknown>;
  function field(
    group: string,
    key: string,
    label: string,
    options: {
      type?: string;
      hint?: string;
      min?: number;
      max?: number;
      env?: boolean;
      list?: boolean;
    } = {},
  ) {
    const object = (config[group] || {}) as Record<string, unknown>;
    const raw = object[key];
    const value =
      options.list && listDrafts[`${group}.${key}`] !== undefined
        ? listDrafts[`${group}.${key}`]
        : Array.isArray(raw)
          ? raw.join(", ")
          : typeof raw === "string" || typeof raw === "number"
            ? raw
            : "";
    return (
      <label key={`${group}.${key}`} htmlFor={`${group}-${key}`}>
        {label}
        <input
          id={`${group}-${key}`}
          type={options.type || "text"}
          value={value}
          min={options.min}
          max={options.max}
          pattern={options.env ? "[A-Za-z_][A-Za-z0-9_]*" : undefined}
          autoComplete="off"
          onChange={(e) => {
            const text = e.target.value;
            if (options.list)
              setListDrafts({ ...listDrafts, [`${group}.${key}`]: text });
            onChange({
              ...config,
              [group]: {
                ...object,
                [key]: options.list
                  ? text
                      .split(",")
                      .map((x) => x.trim())
                      .filter(Boolean)
                  : options.type === "number"
                    ? Number(text)
                    : text,
              },
            });
          }}
        />
        {options.hint && <span className="field-hint">{options.hint}</span>}
      </label>
    );
  }
  return (
    <div className="configuration-fields">
      <details className="settings-group" open>
        <summary>Models and worker defaults</summary>
        <p className="field-hint">
          Enter environment variable names for credentials. Never paste a token
          or API key. Connection changes may require restarting the daemon.
        </p>
        <ModelSettings
          config={config}
          onChange={onChange}
          group="model"
          title="Assistant"
        />
        <ModelSettings
          config={config}
          onChange={onChange}
          group="worker_model"
          title="Worker"
          legend="Default model for new workers"
        />
        <WorkerModelOverrides config={config} projects={projects} />
      </details>
      <details className="settings-group">
        <summary>Advanced</summary>
        <details className="advanced-connection">
          <summary>Advanced: Slack bot and direct Linear API</summary>
          <p className="field-hint">
            Optional integrations for a dedicated bot identity or direct API
            access. Named CLI connections above are the simpler starting point.
            Bot connection changes require a daemon restart.
          </p>
          <div className="config-field-group">
            <h3>Slack bot</h3>
            {field("slack", "owner_user_id", "Your Slack user ID")}
            {field("slack", "bot_token_env", "Bot token environment variable", {
              env: true,
            })}
            {field("slack", "app_token_env", "App token environment variable", {
              env: true,
            })}
          </div>
          <div className="config-field-group">
            <h3>Linear</h3>
            <label className="profile-choice">
              <input
                type="checkbox"
                checked={linear.import_assignments === true}
                onChange={(e) =>
                  onChange({
                    ...config,
                    linear: {
                      ...linear,
                      import_assignments: e.target.checked,
                    },
                  })
                }
                aria-describedby="linear-import-hint"
              />
              <span>Import assigned issues as projects</span>
            </label>
            <p className="field-hint" id="linear-import-hint">
              Optional. Import your assigned issues from the teams below using
              the direct API. Projects in crew-assistant do not require Linear;
              keep this off unless you want automatic imports from this account.
              Configured Linear CLI connections take precedence; enable imports
              on those connections instead.
            </p>
            {field("linear", "api_key_env", "API key environment variable", {
              env: true,
            })}
            {field("linear", "team_ids", "Watched team IDs", {
              list: true,
              hint: "Separate IDs with commas. Configure only the teams you want the assistant to access.",
            })}
          </div>
        </details>
      </details>
      <details className="settings-group">
        <summary>Capacity and recovery</summary>
        <p className="field-hint">
          These limits apply across coordinated work. Model call counts are an
          operating limit, not a dollar budget.
        </p>
        <div className="config-field-group">
          {field("limits", "max_agents", "Maximum active agents", {
            type: "number",
            min: 1,
            max: 64,
          })}
          {field("limits", "max_depth", "Maximum delegation depth", {
            type: "number",
            min: 1,
            max: 10,
          })}
          {field(
            "limits",
            "max_model_calls_per_day",
            "Maximum model calls per day",
            { type: "number", min: 1, max: 100000 },
          )}
          {field(
            "limits",
            "max_model_turns",
            "Maximum model turns per request",
            { type: "number", min: 1, max: 32 },
          )}
          {field(
            "limits",
            "check_in_minutes",
            "Expected agent check-in (minutes)",
            { type: "number", min: 1, max: 1440 },
          )}
          {field("limits", "max_recoveries", "Maximum recovery attempts", {
            type: "number",
            min: 0,
            max: 10,
          })}
        </div>
        <WorkerUsageSettings config={config} onChange={onChange} />
      </details>
    </div>
  );
}
