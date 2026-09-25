import { section, type Config } from "./api";
import { modelLabel, useModelCatalog, type ModelOption } from "./modelCatalog";

const group = "model";

export function ModelSettings({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const model = section(config[group]);
  const value = (key: string) => String(model[key] ?? "");
  const change = (key: string, next: string | number) =>
    onChange({ ...config, [group]: { ...model, [key]: next } });
  const codex = value("engine") === "codex";
  const claude = value("engine") === "claude";
  const localCLI = codex || claude;
  const { catalog, options, error, loading, refresh } = useModelCatalog(
    localCLI ? value("engine") : "",
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
          value={value("engine")}
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
            Optional. The list above uses the saved login and program, so save
            changes here before refreshing it.
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
              <label htmlFor={`${group}-codex_bin`}>
                Codex program
                <input
                  id={`${group}-codex_bin`}
                  value={value("codex_bin")}
                  onChange={(e) => change("codex_bin", e.target.value)}
                />
              </label>
              <label htmlFor={`${group}-codex_home`}>
                Codex folder
                <input
                  id={`${group}-codex_home`}
                  value={value("codex_home")}
                  onChange={(e) => change("codex_home", e.target.value)}
                  placeholder="A folder of its own, as an absolute path"
                  autoComplete="off"
                  required
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
              <label htmlFor={`${group}-claude_bin`}>
                Claude program
                <input
                  id={`${group}-claude_bin`}
                  value={value("claude_bin")}
                  onChange={(e) => change("claude_bin", e.target.value)}
                />
              </label>
              <label htmlFor={`${group}-claude_home`}>
                Claude settings folder
                <input
                  id={`${group}-claude_home`}
                  value={value("claude_home")}
                  onChange={(e) => change("claude_home", e.target.value)}
                  autoComplete="off"
                />
                <span className="hint">Uses your existing Claude login.</span>
              </label>
            </>
          ) : (
            <>
              <label htmlFor={`${group}-base_url`}>
                API address
                <input
                  type="url"
                  id={`${group}-base_url`}
                  value={value("base_url")}
                  onChange={(e) => change("base_url", e.target.value)}
                />
              </label>
              <label htmlFor={`${group}-api_key_env`}>
                API key variable
                <input
                  id={`${group}-api_key_env`}
                  value={value("api_key_env")}
                  onChange={(e) => change("api_key_env", e.target.value)}
                  pattern="[A-Za-z_][A-Za-z0-9_]*"
                  autoComplete="off"
                />
                <span className="hint">
                  The environment variable's name, never the key.
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
