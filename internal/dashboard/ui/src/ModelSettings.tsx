import { section, withEngine, type Config, type ConfigDefaults } from "./api";
import { modelLabel, useModelCatalog, type ModelOption } from "./modelCatalog";

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
  const engine = value("engine");
  // An API lists no models; its model is typed in.
  const api = engine === "openai-compatible";
  const { catalog, options, error, loading, refresh } = useModelCatalog(
    api ? "" : engine,
  );
  const selected = options.find((option) => option.id === value("model"));
  const efforts = selected?.efforts || [];
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
          <option value="codex">A model on Codex</option>
          <option value="claude">A model on Claude</option>
          <option value="openai-compatible">A model on another API</option>
        </select>
        <span className="hint">
          {engine
            ? "Every suggestion and loading line uses this model, and no other."
            : "Luna on your Codex login, or Haiku on your Claude login: your assistant's engine first, then the other if that isn't working."}
        </span>
      </label>
      {api && (
        <>
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
        </>
      )}
      {engine && !api && (
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
          </label>
          {selected?.description && (
            <p className="hint">{selected.description}</p>
          )}
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
                {selected?.default_effort
                  ? ` (${selected.default_effort})`
                  : ""}
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
          <p className="hint" role="status">
            {loading ? "Finding models…" : error || catalog?.detail}
          </p>
          {catalog?.available && !selected && value("model") && (
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
  return (
    <div className="form">
      <p className="hint">
        Optional; a blank field uses what it shows. Lists of models use the
        saved login and program, so save changes here before refreshing one.
      </p>
      <h3>Codex</h3>
      <label htmlFor="engines-codex-bin">
        Codex program
        <input
          id="engines-codex-bin"
          value={setting("codex", "bin")}
          onChange={(e) => change("codex", "bin", e.target.value)}
          placeholder={defaults?.engines?.codex?.bin}
          autoComplete="off"
        />
      </label>
      <label htmlFor="engines-codex-home">
        Codex folder
        <input
          id="engines-codex-home"
          value={setting("codex", "home")}
          onChange={(e) => change("codex", "home", e.target.value)}
          placeholder={defaults?.engines?.codex?.home}
          autoComplete="off"
        />
        <span className="hint">
          Codex keeps its settings, login and sessions here; use one without
          global AGENTS files. After changing it, sign in with{" "}
          <code>crew-assistant model login</code>.
        </span>
      </label>
      <h3>Claude</h3>
      <label htmlFor="engines-claude-bin">
        Claude program
        <input
          id="engines-claude-bin"
          value={setting("claude", "bin")}
          onChange={(e) => change("claude", "bin", e.target.value)}
          placeholder={defaults?.engines?.claude?.bin}
          autoComplete="off"
        />
      </label>
      <label htmlFor="engines-claude-home">
        Claude settings folder
        <input
          id="engines-claude-home"
          value={setting("claude", "home")}
          onChange={(e) => change("claude", "home", e.target.value)}
          placeholder={defaults?.engines?.claude?.home}
          autoComplete="off"
        />
        <span className="hint">Uses your existing Claude login.</span>
      </label>
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
    </div>
  );
}

function effortsFor(model: ModelOption, saved: string): string {
  if (!saved || model.efforts.some((effort) => effort.id === saved))
    return saved;
  return model.default_effort;
}
