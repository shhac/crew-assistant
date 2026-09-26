import { modelLabel, useModelCatalog } from "./modelCatalog";

export interface ModelChoice {
  engine: string;
  model: string;
  effort: string;
}

const cliEngines = new Set(["codex", "claude"]);

/**
 * The engine, model and effort someone on the team runs on, a member or an
 * assistant. A CLI engine's models are listed as its login reports them; an
 * API's model is typed in.
 */
export function ModelFields({
  id,
  engines,
  value,
  saved,
  onChange,
}: {
  /** Prefixes the fields' ids: member or assistant. */
  id: string;
  engines: { id: string; label: string }[];
  value: ModelChoice;
  /** What is saved, which comes back on going back to its engine. */
  saved?: { engine: string; model?: string };
  onChange: (next: ModelChoice) => void;
}) {
  const { engine, model, effort } = value;
  const cli = cliEngines.has(engine);
  const models = useModelCatalog(cli ? engine : "");
  const listed = models.options.some((option) => option.id === model);
  // Another engine's models don't run here; going back to the saved engine
  // brings back the saved model.
  const chooseEngine = (next: string) =>
    onChange({
      ...value,
      engine: next,
      model: next === saved?.engine ? (saved.model ?? "") : "",
    });
  return (
    <>
      <div className="form-row">
        <label htmlFor={`${id}-engine`}>
          Engine
          <select
            id={`${id}-engine`}
            className="field"
            value={engine}
            onChange={(e) => chooseEngine(e.target.value)}
          >
            {engines.map((option) => (
              <option key={option.id} value={option.id}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        {cli ? (
          <label htmlFor={`${id}-model`}>
            Model
            <select
              id={`${id}-model`}
              className="field"
              value={model}
              onChange={(e) => onChange({ ...value, model: e.target.value })}
              disabled={models.loading}
            >
              <option value="">The engine's default</option>
              {model && !listed && (
                <option value={model}>{model} (saved)</option>
              )}
              {models.options.map((option) => (
                <option key={option.id} value={option.id}>
                  {modelLabel(option, models.catalog)}
                </option>
              ))}
            </select>
            <span className="hint" role="status">
              {models.loading
                ? "Finding models…"
                : models.error || models.catalog?.detail}
            </span>
            {models.catalog?.available && model && !listed && (
              <span className="hint">
                The saved model isn't in this list. It stays until you pick
                another.
              </span>
            )}
          </label>
        ) : (
          <label htmlFor={`${id}-model`}>
            Model
            <input
              id={`${id}-model`}
              className="field"
              value={model}
              maxLength={80}
              autoComplete="off"
              onChange={(e) => onChange({ ...value, model: e.target.value })}
            />
            <span className="hint">The model's id at the API.</span>
          </label>
        )}
        <label htmlFor={`${id}-effort`}>
          Reasoning effort
          <input
            id={`${id}-effort`}
            className="field"
            value={effort}
            maxLength={20}
            placeholder="high"
            onChange={(e) => onChange({ ...value, effort: e.target.value })}
          />
          <span className="hint">Optional.</span>
        </label>
      </div>
      {cli && (
        <div className="actions">
          <button
            className="btn btn-sm"
            type="button"
            disabled={models.loading}
            onClick={models.refresh}
          >
            Refresh the list of models
          </button>
        </div>
      )}
    </>
  );
}
