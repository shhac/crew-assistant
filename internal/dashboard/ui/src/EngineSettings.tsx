import type { ReactNode } from "react";
import {
  section,
  withEngine,
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
    </div>
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
