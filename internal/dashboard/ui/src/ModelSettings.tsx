import { useEffect, useState } from "react";
import { api, type Config } from "./api";

export type ModelOption = {
  id: string;
  name: string;
  description?: string;
  default_effort: string;
  efforts: { id: string; description?: string }[];
  is_default: boolean;
};
export type Catalog = {
  available: boolean;
  detail: string;
  engine: string;
  models: ModelOption[];
  current: { model: string; effort: string };
  default: { model: string; effort: string };
};

export function ModelSettings({
  config,
  onChange,
  group,
  title,
  legend,
}: {
  config: Config;
  onChange: (value: Config) => void;
  group: "model" | "worker_model";
  title: string;
  /** Group heading, when the owner-facing name differs from the field prefix. */
  legend?: string;
}) {
  const model = (config[group] || {}) as Record<string, unknown>;
  const value = (key: string) => String(model[key] ?? "");
  const change = (key: string, next: string | number) =>
    onChange({ ...config, [group]: { ...model, [key]: next } });
  const codex = value("engine") === "codex";
  const claude = value("engine") === "claude";
  const localCLI = codex || claude;
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (!localCLI) {
      setCatalog(null);
      return;
    }
    let active = true;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setCatalog(null);
    api<Catalog>(
      `/api/models?profile=${group === "model" ? "assistant" : "worker"}&engine=${value("engine")}`,
      { signal: controller.signal },
    )
      .then((data) => {
        if (active) setCatalog(data);
      })
      .catch(() => {
        if (active)
          setError(
            "Could not load model options. Your saved selection is unchanged.",
          );
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [codex, claude, group, revision]);
  const options =
    catalog?.available && catalog.engine === value("engine")
      ? catalog.models
      : [];
  const selected = options.find((option) => option.id === value("model"));
  const efforts = selected?.efforts || [];
  const chooseModel = (id: string) => {
    const option = options.find((item) => item.id === id);
    if (!option) return;
    const effort = effortsFor(option, value("effort"));
    onChange({ ...config, [group]: { ...model, model: id, effort } });
  };
  return (
    <fieldset className="config-field-group">
      <legend>{legend || `${title} model`}</legend>
      <p className="field-hint">
        {group === "model"
          ? "Used for conversation, coordination and acceptance reviews. The recommended selection is ready to use."
          : "Used for delegated work. The assistant manages worker setup; model choices are optional."}
      </p>
      <label htmlFor={`${group}-engine`}>
        {title} engine
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
          <option value="codex">Codex CLI</option>
          <option value="claude">Claude Code CLI</option>
          <option value="openai-compatible">Custom API (advanced)</option>
        </select>
      </label>
      {localCLI && (
        <>
          <label htmlFor={`${group}-model`}>
            {title} model
            <select
              id={`${group}-model`}
              value={value("model")}
              onChange={(e) => chooseModel(e.target.value)}
              disabled={loading || options.length === 0}
            >
              {!selected && (
                <option value={value("model")}>
                  {value("model")
                    ? `${value("model")} (saved selection)`
                    : "Choose a model"}
                </option>
              )}
              {options.map((option) => (
                <option key={option.id} value={option.id}>
                  {option.name}
                  {option.id === catalog?.default.model
                    ? " — recommended"
                    : option.is_default
                      ? " — CLI default"
                      : ""}
                </option>
              ))}
            </select>
          </label>
          {selected?.description && (
            <p className="field-hint">{selected.description}</p>
          )}
          <label htmlFor={`${group}-effort`}>
            {title} reasoning effort
            <select
              id={`${group}-effort`}
              value={value("effort")}
              onChange={(e) => change("effort", e.target.value)}
              disabled={loading || !selected}
            >
              <option value="">
                Model default
                {selected?.default_effort
                  ? ` (${selected.default_effort})`
                  : ""}
              </option>
              {value("effort") &&
                !efforts.some((effort) => effort.id === value("effort")) && (
                  <option value={value("effort")}>
                    {value("effort")} (saved selection)
                  </option>
                )}
              {efforts.map((effort) => (
                <option key={effort.id} value={effort.id}>
                  {effort.id}
                  {effort.id === selected?.default_effort
                    ? " — model default"
                    : ""}
                </option>
              ))}
            </select>
          </label>
          {efforts.find((effort) => effort.id === value("effort"))
            ?.description && (
            <p className="field-hint">
              {
                efforts.find((effort) => effort.id === value("effort"))
                  ?.description
              }
            </p>
          )}
          <p className="field-hint" role="status">
            {loading
              ? "Looking up available models…"
              : error || catalog?.detail}
          </p>
          {catalog?.available && !selected && value("model") && (
            <p className="field-hint">
              Your saved model is not in this catalog. Choose an available model
              when ready; it has not been changed.
            </p>
          )}
          <button
            className="secondary"
            type="button"
            disabled={loading}
            onClick={() => setRevision(revision + 1)}
          >
            Refresh model options
          </button>
        </>
      )}
      <details>
        <summary>Advanced model settings</summary>
        <p className="field-hint">
          These are optional overrides. Model discovery uses the saved login and
          executable; save changes here before refreshing options.
        </p>
        <label htmlFor={`${group}-manual-model`}>
          {title} custom model identifier
          <input
            id={`${group}-manual-model`}
            value={value("model")}
            onChange={(e) => change("model", e.target.value)}
            autoComplete="off"
          />
        </label>
        <label htmlFor={`${group}-manual-effort`}>
          {title} custom reasoning effort
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
              {title} Codex executable
              <input
                id={`${group}-codex_bin`}
                value={value("codex_bin")}
                onChange={(e) => change("codex_bin", e.target.value)}
              />
            </label>
            <label htmlFor={`${group}-codex_home`}>
              {title} Codex home
              <input
                id={`${group}-codex_home`}
                value={value("codex_home")}
                onChange={(e) => change("codex_home", e.target.value)}
                placeholder="Absolute path to a dedicated Codex directory"
                autoComplete="off"
                required
              />
              <span className="field-hint">
                Configuration, login and session data stay here. After saving a
                new path, sign in with{" "}
                <code>
                  crew-assistant model login
                  {group === "worker_model" ? " --profile worker" : ""}
                </code>
                .
              </span>
            </label>
            <p className="field-hint">
              Use a dedicated directory without global AGENTS files. Daemon turn
              limits and process time/output bounds apply.
            </p>
          </>
        ) : claude ? (
          <>
            <label htmlFor={`${group}-claude_bin`}>
              {title} Claude executable
              <input
                id={`${group}-claude_bin`}
                value={value("claude_bin")}
                onChange={(e) => change("claude_bin", e.target.value)}
              />
            </label>
            <label htmlFor={`${group}-claude_home`}>
              {title} Claude configuration directory
              <input
                id={`${group}-claude_home`}
                value={value("claude_home")}
                onChange={(e) => change("claude_home", e.target.value)}
                autoComplete="off"
              />
              <span className="field-hint">
                Uses the existing Claude CLI login. Leave the default to share
                that login with workers.
              </span>
            </label>
          </>
        ) : (
          <>
            <label htmlFor={`${group}-base_url`}>
              {title} provider API base URL
              <input
                type="url"
                id={`${group}-base_url`}
                value={value("base_url")}
                onChange={(e) => change("base_url", e.target.value)}
              />
            </label>
            <label htmlFor={`${group}-api_key_env`}>
              {title} API key environment variable
              <input
                id={`${group}-api_key_env`}
                value={value("api_key_env")}
                onChange={(e) => change("api_key_env", e.target.value)}
                pattern="[A-Za-z_][A-Za-z0-9_]*"
                autoComplete="off"
              />
              <span className="field-hint">
                Enter a variable name, never its secret value.
              </span>
            </label>
            <label htmlFor={`${group}-max_tokens`}>
              {title} maximum output tokens per call
              <input
                type="number"
                min={128}
                max={group === "worker_model" ? 32768 : 131072}
                id={`${group}-max_tokens`}
                value={value("max_tokens")}
                onChange={(e) => change("max_tokens", Number(e.target.value))}
              />
            </label>
          </>
        )}
      </details>
    </fieldset>
  );
}

function effortsFor(model: ModelOption, saved: string): string {
  if (!saved || model.efforts.some((effort) => effort.id === saved))
    return saved;
  return model.default_effort;
}
