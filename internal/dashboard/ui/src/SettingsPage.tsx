import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { AssistantSetup } from "./AssistantSetup";
import { ConnectionsSettings } from "./ConnectionsSettings";
import { ChatSettings } from "./ChatSettings";
import { ModelSettings } from "./ModelSettings";
import { ThemePicker } from "./Identity";
import {
  ErrorNotice,
  humanStatus,
  Icon,
  Mark,
  PageHeading,
  Status,
} from "./ui";
import { api, errorText, section, type Config, type State } from "./api";

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
            config={config}
            onChange={(next) => {
              setConfig(next);
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
          <h2>Pause</h2>
        </div>
        <p className="section-description">
          Pausing stops teams from starting their next step. A step already
          running finishes first.
        </p>
        {control}
      </section>
      <section className="settings-boundaries">
        <Icon name="Lock" size={20} />
        <div>
          <h3>Built-in boundaries</h3>
          <p>
            The assistant coordinates your projects. It cannot write project
            code, deploy, access production data, or buy things. A personality
            change cannot override these boundaries.
          </p>
        </div>
      </section>
    </section>
  );
}

function ConfigurationFields({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const [listDrafts, setListDrafts] = useState<Record<string, string>>({});
  const linear = section(config.linear);
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
    const object = section(config[group]);
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
        <summary>Model</summary>
        <p className="field-hint">
          Enter environment variable names for credentials. Never paste a token
          or API key. Connection changes may require restarting the daemon.
        </p>
        <ModelSettings config={config} onChange={onChange} />
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
        <summary>Limits</summary>
        <p className="field-hint">
          Model call counts are an operating limit, not a dollar budget.
        </p>
        <div className="config-field-group">
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
        </div>
        <RoleUsageFields config={config} onChange={onChange} />
      </details>
    </div>
  );
}

const usageEngines = [
  ["codex_max_used_percent", "Hold Codex roles above (% of subscription used)"],
  [
    "claude_max_used_percent",
    "Hold Claude roles above (% of subscription used)",
  ],
] as const;

/**
 * Team roles wait, rather than fail, while a subscription is nearly used up.
 * The thresholds live one level deeper than the other limits.
 */
function RoleUsageFields({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const limits = section(config.limits);
  const usage = section(limits.role_usage);
  const set = (key: string, value: unknown) =>
    onChange({
      ...config,
      limits: { ...limits, role_usage: { ...usage, [key]: value } },
    });
  return (
    <div className="config-field-group">
      {usageEngines.map(([key, label]) => (
        <label key={key} htmlFor={`role-usage-${key}`}>
          {label}
          <input
            id={`role-usage-${key}`}
            type="number"
            min={0}
            max={100}
            value={numberOrEmpty(usage[key])}
            onChange={(e) => set(key, Number(e.target.value))}
          />
        </label>
      ))}
      <label htmlFor="role-usage-unavailable">
        When usage can't be checked
        <select
          id="role-usage-unavailable"
          value={usage.on_unavailable === "pause" ? "pause" : "allow"}
          onChange={(e) => set("on_unavailable", e.target.value)}
        >
          <option value="allow">Carry on</option>
          <option value="pause">Wait until it can be</option>
        </select>
      </label>
      <p className="field-hint">
        A held role waits for its usage window to reset and then carries on by
        itself. 0 turns the hold off for that engine.
      </p>
    </div>
  );
}

function numberOrEmpty(value: unknown) {
  return typeof value === "number" ? value : "";
}
