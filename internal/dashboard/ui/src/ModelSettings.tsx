import type { ReactNode } from "react";
import {
  section,
  withEngine,
  type Config,
  type ConfigDefaults,
  type EffortParameter,
  type EngineChoice,
} from "./api";
import {
  choiceFor,
  choicesFor,
  engineOptions,
  smallLogins,
  useEngineChoices,
} from "./engines";
import {
  effortsListed,
  modelLabel,
  useModelCatalog,
  type ModelOption,
} from "./modelCatalog";

const group = "suggestions";

/**
 * The model that suggests the owner's next message and writes loading
 * lines: the small models unless the owner picks another, on a CLI login or
 * the API.
 */
export function SuggestionModel({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const models = section(config.models);
  const chosen = section(models[group]);
  const value = (key: string) => String(chosen[key] ?? "");
  const set = (next: Record<string, unknown>) =>
    onChange({
      ...config,
      models: { ...models, [group]: { ...chosen, ...next } },
    });
  const choices = useEngineChoices();
  const engine = value("engine");
  const choice = choiceFor(engine, choices);
  const listable = !!choice?.models;
  const api = choice?.cli === false;
  const { catalog, options, error, loading, refresh } = useModelCatalog(
    listable ? engine : "",
  );
  // An API's model is typed in until its list offers some; a CLI keeps its
  // list, which offers the saved model while it can't be read.
  const typed = !listable || (api && options.length === 0);
  const selected = options.find((option) => option.id === value("model"));
  const efforts = selected?.efforts || [];
  const pickEffort =
    !typed && !!choice?.efforts && (!selected || effortsListed(selected));
  const chooseModel = (id: string) => {
    const option = options.find((item) => item.id === id);
    if (!option) return;
    set({ model: id, effort: effortsFor(option, value("effort")) });
  };
  return (
    <div className="form">
      <label htmlFor={`${group}-engine`}>
        Suggestions and loading lines
        <select
          id={`${group}-engine`}
          value={engine}
          onChange={(e) =>
            set({ engine: e.target.value, model: "", effort: "" })
          }
        >
          <option value="">The small models (recommended)</option>
          {engineOptions(choices, "small", engine).map((option) => (
            <option key={option.id} value={option.id}>
              A model on {engineMention(option.id, option.label, choices)}
            </option>
          ))}
        </select>
        <span className="hint">
          {engine
            ? "Every suggestion and loading line uses this model, and no other."
            : smallModelsHint(smallLogins(choices))}
        </span>
      </label>
      {engine && typed && (
        <label htmlFor={`${group}-model`}>
          Model
          <input
            id={`${group}-model`}
            value={value("model")}
            maxLength={80}
            autoComplete="off"
            onChange={(e) => set({ model: e.target.value.trim() })}
          />
          <span className="hint">
            The model's id at the API address under Engines. Each call is kept
            to a short reply and billed by the API.
          </span>
        </label>
      )}
      {engine && !typed && (
        <>
          <label htmlFor={`${group}-model`}>
            Model
            <select
              id={`${group}-model`}
              value={value("model")}
              onChange={(e) => chooseModel(e.target.value)}
              disabled={loading || options.length === 0}
            >
              {!selected && (
                <option value={value("model")}>
                  {value("model")
                    ? `${value("model")} (saved)`
                    : "Choose a model"}
                </option>
              )}
              {options.map((option) => (
                <option key={option.id} value={option.id}>
                  {modelLabel(option, catalog)}
                </option>
              ))}
            </select>
            {api && (
              <span className="hint">
                Each call is kept to a short reply and billed by the API.
              </span>
            )}
          </label>
          {selected?.description && (
            <p className="hint">{selected.description}</p>
          )}
        </>
      )}
      {engine && pickEffort && (
        <label htmlFor={`${group}-effort`}>
          Reasoning effort
          <select
            id={`${group}-effort`}
            value={value("effort")}
            onChange={(e) => set({ effort: e.target.value })}
            disabled={loading || !selected}
          >
            <option value="">
              The model's default
              {selected?.default_effort ? ` (${selected.default_effort})` : ""}
            </option>
            {value("effort") &&
              !efforts.some((effort) => effort.id === value("effort")) && (
                <option value={value("effort")}>
                  {value("effort")} (saved)
                </option>
              )}
            {efforts.map((effort) => (
              <option key={effort.id} value={effort.id}>
                {effort.id}
                {effort.id === selected?.default_effort ? " (default)" : ""}
              </option>
            ))}
          </select>
        </label>
      )}
      {engine && !pickEffort && (
        <label htmlFor={`${group}-effort`}>
          Reasoning effort
          <input
            id={`${group}-effort`}
            value={value("effort")}
            maxLength={20}
            placeholder="low"
            autoComplete="off"
            onChange={(e) => set({ effort: e.target.value.trim() })}
          />
          <span className="hint">Optional.</span>
        </label>
      )}
      {engine && listable && (
        <>
          <p className="hint" role="status">
            {loading ? "Finding models…" : error || catalog?.detail}
          </p>
          {catalog?.available && !selected && value("model") && !typed && (
            <p className="hint">
              Your saved model isn't in this list. It stays until you pick
              another.
            </p>
          )}
          <div className="actions">
            <button
              className="btn btn-sm"
              type="button"
              disabled={loading}
              onClick={refresh}
            >
              Refresh the list
            </button>
          </div>
        </>
      )}
    </div>
  );
}

/** "another API" reads mid-sentence; a CLI keeps its name. */
function engineMention(
  engine: string,
  label: string,
  choices: readonly EngineChoice[],
) {
  if (choiceFor(engine, choices)?.cli !== false) return label;
  return label.charAt(0).toLowerCase() + label.slice(1);
}

/** "Luna on your Codex login, or Haiku on your Claude login: …" */
function smallModelsHint(logins: ReturnType<typeof smallLogins>) {
  if (logins.length === 0) return "";
  const each = logins
    .map((login) => `${login.model.name} on your ${login.label} login`)
    .join(", or ");
  if (logins.length === 1) return `${each}.`;
  const rest = logins.length > 2 ? "the others" : "the other";
  return `${each}: your assistant's engine first, then ${rest} if that isn't working.`;
}

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

function effortsFor(model: ModelOption, saved: string): string {
  if (!saved || model.efforts.some((effort) => effort.id === saved))
    return saved;
  return model.default_effort ?? "";
}
