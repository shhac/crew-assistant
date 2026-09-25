import { section, withEngine, type Config, type ConfigDefaults } from "./api";
import { modelLabel, useModelCatalog, type ModelOption } from "./modelCatalog";

const group = "model";

export function ModelSettings({
  config,
  defaults,
  onChange,
}: {
  config: Config;
  defaults?: ConfigDefaults;
  onChange: (value: Config) => void;
}) {
  const model = section(config[group]);
  const value = (key: string) => String(model[key] ?? "");
  const change = (key: string, next: string | number) =>
    onChange({ ...config, [group]: { ...model, [key]: next } });
  const engine = value("engine");
  const engineSettings = section(section(config.engines)[engine]);
  const setting = (key: string) => String(engineSettings[key] ?? "");
  const changeSetting = (key: string, next: string) =>
    onChange(withEngine(config, engine, { [key]: next }));
  const codex = engine === "codex";
  const claude = engine === "claude";
  const localCLI = codex || claude;
  const { catalog, options, error, loading, refresh } = useModelCatalog(
    localCLI ? engine : "",
  );
  const selected = options.find((option) => option.id === value("model"));
  const efforts = selected?.efforts || [];
  const chooseModel = (id: string) => {
    const option = options.find((item) => item.id === id);
    if (!option) return;
    const effort = effortsFor(option, value("effort"));
    onChange({ ...config, [group]: { ...model, model: id, effort } });
  };
  return (
    <div className="form">
      <label htmlFor={`${group}-engine`}>
        Runs on
        <select
          id={`${group}-engine`}
          value={engine}
          onChange={(e) =>
            onChange({
              ...config,
              [group]: {
                ...model,
                engine: e.target.value,
                model: "",
                effort: "",
              },
            })
          }
        >
          <option value="codex">Codex</option>
          <option value="claude">Claude</option>
          <option value="openai-compatible">Another API</option>
        </select>
      </label>
      {localCLI && (
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
              onChange={(e) => change("effort", e.target.value)}
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
          {efforts.find((effort) => effort.id === value("effort"))
            ?.description && (
            <p className="hint">
              {
                efforts.find((effort) => effort.id === value("effort"))
                  ?.description
              }
            </p>
          )}
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
      <details className="disclosure">
        <summary>More model settings</summary>
        <div className="form disclosure-body">
          <p className="hint">
            Optional; a blank field uses what it shows. The list above uses the
            saved login and program, so save changes here before refreshing it.
          </p>
          <label htmlFor={`${group}-manual-model`}>
            Model ID
            <input
              id={`${group}-manual-model`}
              value={value("model")}
              onChange={(e) => change("model", e.target.value)}
              autoComplete="off"
            />
          </label>
          <label htmlFor={`${group}-manual-effort`}>
            Reasoning effort
            <input
              id={`${group}-manual-effort`}
              value={value("effort")}
              onChange={(e) => change("effort", e.target.value)}
              autoComplete="off"
            />
          </label>
          {codex ? (
            <>
              <label htmlFor="engines-codex-bin">
                Codex program
                <input
                  id="engines-codex-bin"
                  value={setting("bin")}
                  onChange={(e) => changeSetting("bin", e.target.value)}
                  placeholder={defaults?.engines?.codex?.bin}
                  autoComplete="off"
                />
              </label>
              <label htmlFor="engines-codex-home">
                Codex folder
                <input
                  id="engines-codex-home"
                  value={setting("home")}
                  onChange={(e) => changeSetting("home", e.target.value)}
                  placeholder={defaults?.engines?.codex?.home}
                  autoComplete="off"
                />
                <span className="hint">
                  Codex keeps its settings, login and sessions here; use one
                  without global AGENTS files. After changing it, sign in with{" "}
                  <code>crew-assistant model login</code>.
                </span>
              </label>
            </>
          ) : claude ? (
            <>
              <label htmlFor="engines-claude-bin">
                Claude program
                <input
                  id="engines-claude-bin"
                  value={setting("bin")}
                  onChange={(e) => changeSetting("bin", e.target.value)}
                  placeholder={defaults?.engines?.claude?.bin}
                  autoComplete="off"
                />
              </label>
              <label htmlFor="engines-claude-home">
                Claude settings folder
                <input
                  id="engines-claude-home"
                  value={setting("home")}
                  onChange={(e) => changeSetting("home", e.target.value)}
                  placeholder={defaults?.engines?.claude?.home}
                  autoComplete="off"
                />
                <span className="hint">Uses your existing Claude login.</span>
              </label>
            </>
          ) : (
            <>
              <label htmlFor="engines-openai-compatible-base_url">
                API address
                <input
                  type="url"
                  id="engines-openai-compatible-base_url"
                  value={setting("base_url")}
                  onChange={(e) => changeSetting("base_url", e.target.value)}
                  placeholder={defaults?.openai_base_url}
                />
              </label>
              <label htmlFor="engines-openai-compatible-api_key_env">
                API key variable
                <input
                  id="engines-openai-compatible-api_key_env"
                  value={setting("api_key_env")}
                  onChange={(e) => changeSetting("api_key_env", e.target.value)}
                  pattern="[A-Za-z_][A-Za-z0-9_]*"
                  autoComplete="off"
                />
                <span className="hint">
                  The environment variable's name, never the key. Blank sends no
                  key.
                </span>
              </label>
              <label htmlFor={`${group}-max_tokens`}>
                Most output tokens per call
                <input
                  type="number"
                  min={128}
                  max={131072}
                  id={`${group}-max_tokens`}
                  value={value("max_tokens")}
                  onChange={(e) => change("max_tokens", Number(e.target.value))}
                />
              </label>
            </>
          )}
        </div>
      </details>
    </div>
  );
}

function effortsFor(model: ModelOption, saved: string): string {
  if (!saved || model.efforts.some((effort) => effort.id === saved))
    return saved;
  return model.default_effort;
}
