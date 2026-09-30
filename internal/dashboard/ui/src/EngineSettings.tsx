import type { ReactNode } from "react";
import {
  legacyProvider,
  section,
  withEngine,
  type APIProviderSettings,
  type Config,
  type ConfigDefaults,
  type EffortParameter,
  type EngineChoice,
} from "./api";
import { choicesFor, useEngineChoices } from "./engines";

/**
 * How the daemon reaches each engine, for every assistant, member and small
 * model alike: the CLIs and their logins, and the API's address and key.
 */
export function EngineSettings({
  config,
  defaults,
  onChange,
}: {
  config: Config;
  defaults?: ConfigDefaults;
  onChange: (value: Config) => void;
}) {
  const setting = (engine: string, key: string) =>
    String(section(section(config.engines)[engine])[key] ?? "");
  const change = (engine: string, key: string, next: string) =>
    onChange(withEngine(config, engine, { [key]: next }));
  const choices = useEngineChoices();
  const effortParameter: EffortParameter =
    setting("openai-compatible", "effort_parameter") === "reasoning.effort"
      ? "reasoning.effort"
      : "";
  return (
    <div className="form">
      <p className="hint">
        Optional; a blank field uses what it shows. Lists of models use the
        saved login and program, so save changes here before refreshing one.
      </p>
      {choicesFor(choices, "cli").map((choice) => (
        <CLISettings
          key={choice.engine}
          choice={choice}
          defaults={defaults?.engines?.[choice.engine]}
          setting={(key) => setting(choice.engine, key)}
          change={(key, next) => change(choice.engine, key, next)}
        />
      ))}
      <h3>Another API</h3>
      <label htmlFor="engines-openai-compatible-base_url">
        API address
        <input
          type="url"
          id="engines-openai-compatible-base_url"
          value={setting("openai-compatible", "base_url")}
          onChange={(e) =>
            change("openai-compatible", "base_url", e.target.value)
          }
          placeholder={defaults?.openai_base_url}
        />
      </label>
      <label htmlFor="engines-openai-compatible-api_key_env">
        API key variable
        <input
          id="engines-openai-compatible-api_key_env"
          value={setting("openai-compatible", "api_key_env")}
          onChange={(e) =>
            change("openai-compatible", "api_key_env", e.target.value)
          }
          pattern="[A-Za-z_][A-Za-z0-9_]*"
          autoComplete="off"
        />
        <span className="hint">
          The environment variable's name, never the key. Blank sends no key.
        </span>
      </label>
      <label htmlFor="engines-openai-compatible-effort_parameter">
        Reasoning effort is sent as
        <select
          id="engines-openai-compatible-effort_parameter"
          value={effortParameter}
          onChange={(e) =>
            change("openai-compatible", "effort_parameter", e.target.value)
          }
        >
          <option value="">reasoning_effort</option>
          <option value="reasoning.effort">reasoning.effort</option>
        </select>
        <span className="hint">
          OpenAI and xAI read reasoning_effort; gateways such as Vercel AI
          Gateway and OpenRouter read reasoning.effort.
        </span>
      </label>
      <APIProviders config={config} onChange={onChange} />
    </div>
  );
}

/** A provider's id from its name: lower-case letters, digits and hyphens. */
export function providerID(name: string) {
  const id = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64)
    .replace(/-+$/, "");
  return id === legacyProvider ? `${id}-2` : id;
}

function providersOf(config: Config): APIProviderSettings[] {
  const list = section(config.engines).providers;
  return Array.isArray(list) ? (list as APIProviderSettings[]) : [];
}

/**
 * Further named API providers beside the one above, such as OpenRouter next
 * to a model on this machine. Each is an address, a key variable and where
 * it reads reasoning effort; its id, which saved models name, follows its
 * name until changed.
 */
function APIProviders({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const providers = providersOf(config);
  const save = (next: APIProviderSettings[]) => {
    const engines = { ...section(config.engines) };
    if (next.length === 0) delete engines.providers;
    else engines.providers = next;
    onChange({ ...config, engines });
  };
  const change = (i: number, fields: Partial<APIProviderSettings>) =>
    save(
      providers.map((p, at) => {
        if (at !== i) return p;
        const next = { ...p, ...fields };
        // Drop a blank effort parameter so the default applies.
        if (!next.effort_parameter) delete next.effort_parameter;
        return next;
      }),
    );
  const rename = (i: number, name: string) => {
    const p = providers[i];
    const following = !p.id || p.id === providerID(p.name);
    change(i, following ? { name, id: providerID(name) } : { name });
  };
  return (
    <>
      <h3>More API providers</h3>
      <p className="hint">
        Such as OpenRouter beside a model on this machine. Assistants and
        suggestions can use a model on any of them.
      </p>
      {providers.map((p, i) => {
        const key = `engines-providers-${i}`;
        return (
          <fieldset key={i} className="card form">
            <legend className="sr-only">{p.name || "New API provider"}</legend>
            <div className="panel-head">
              <p>
                <strong>{p.name || "New API provider"}</strong>
              </p>
              <button
                type="button"
                className="btn btn-quiet btn-sm btn-danger"
                onClick={() => save(providers.filter((_, at) => at !== i))}
                aria-label={`Remove API provider ${p.name || i + 1}`}
              >
                Remove
              </button>
            </div>
            <label htmlFor={`${key}-name`}>
              Name
              <input
                id={`${key}-name`}
                value={p.name}
                maxLength={80}
                onChange={(e) => rename(i, e.target.value)}
                autoComplete="off"
              />
            </label>
            <label htmlFor={`${key}-id`}>
              Id
              <input
                id={`${key}-id`}
                value={p.id}
                maxLength={64}
                pattern="[a-z0-9][a-z0-9-]*"
                onChange={(e) => change(i, { id: e.target.value })}
                autoComplete="off"
              />
              <span className="hint">
                What saved models name it by; changing it leaves them on a
                provider that's gone.
              </span>
            </label>
            <label htmlFor={`${key}-base_url`}>
              API address
              <input
                type="url"
                id={`${key}-base_url`}
                value={p.base_url}
                onChange={(e) => change(i, { base_url: e.target.value })}
                placeholder="https://openrouter.ai/api/v1"
              />
            </label>
            <label htmlFor={`${key}-api_key_env`}>
              API key variable
              <input
                id={`${key}-api_key_env`}
                value={p.api_key_env}
                onChange={(e) => change(i, { api_key_env: e.target.value })}
                pattern="[A-Za-z_][A-Za-z0-9_]*"
                autoComplete="off"
              />
              <span className="hint">
                The environment variable's name, never the key. Blank sends no
                key, which only an API on this machine accepts.
              </span>
            </label>
            <label htmlFor={`${key}-effort_parameter`}>
              Reasoning effort is sent as
              <select
                id={`${key}-effort_parameter`}
                value={
                  p.effort_parameter === "reasoning.effort"
                    ? "reasoning.effort"
                    : ""
                }
                onChange={(e) =>
                  change(i, {
                    effort_parameter: e.target.value as EffortParameter,
                  })
                }
              >
                <option value="">reasoning_effort</option>
                <option value="reasoning.effort">reasoning.effort</option>
              </select>
            </label>
          </fieldset>
        );
      })}
      <div className="actions">
        <button
          className="btn btn-sm"
          type="button"
          onClick={() =>
            save([
              ...providers,
              { id: "", name: "", base_url: "", api_key_env: "" },
            ])
          }
        >
          Add an API provider
        </button>
      </div>
    </>
  );
}

/**
 * How a CLI engine's folder is named and explained; one without its own
 * words is described plainly.
 */
const folders: Record<string, { label: string; hint: ReactNode }> = {
  codex: {
    label: "Codex folder",
    hint: (
      <>
        Codex keeps its settings, login and sessions here; use one without
        global AGENTS files. After changing it, sign in with{" "}
        <code>crew-assistant model login</code>.
      </>
    ),
  },
  claude: {
    label: "Claude settings folder",
    hint: "Uses your existing Claude login.",
  },
};

/** One CLI engine's program and the folder its login lives in. */
function CLISettings({
  choice,
  defaults,
  setting,
  change,
}: {
  choice: EngineChoice;
  defaults?: { bin?: string; home?: string };
  setting: (key: string) => string;
  change: (key: string, next: string) => void;
}) {
  const { engine, label } = choice;
  const folder = folders[engine] ?? {
    label: `${label} folder`,
    hint: `${label} keeps its settings and login here. Blank uses its own.`,
  };
  return (
    <>
      <h3>{label}</h3>
      <label htmlFor={`engines-${engine}-bin`}>
        {label} program
        <input
          id={`engines-${engine}-bin`}
          value={setting("bin")}
          onChange={(e) => change("bin", e.target.value)}
          placeholder={defaults?.bin}
          autoComplete="off"
        />
      </label>
      <label htmlFor={`engines-${engine}-home`}>
        {folder.label}
        <input
          id={`engines-${engine}-home`}
          value={setting("home")}
          onChange={(e) => change("home", e.target.value)}
          placeholder={defaults?.home || undefined}
          autoComplete="off"
        />
        <span className="hint">{folder.hint}</span>
      </label>
    </>
  );
}
