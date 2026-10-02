import { useState } from "react";
import {
  choiceFor,
  engineOptions,
  unavailableForRoles,
  useEngineChoices,
  type EngineUse,
} from "./engines";
import { modelLabel, useModelCatalog } from "./modelCatalog";
import { savedProvider, shownProvider, useProviders } from "./providers";

export interface ModelChoice {
  engine: string;
  /** The API provider a model on another API runs on; empty is the first. */
  provider?: string;
  model: string;
  effort: string;
}

/**
 * The engine, model and effort someone on the team runs on, a member or an
 * assistant; on another API, the provider too. An engine's models are listed
 * as it reports them, free ones marked; any model can be typed in instead,
 * and one that can't list them, or an API whose list can't be read, takes its
 * model typed in.
 */
export function ModelFields({
  id,
  use,
  value,
  saved,
  onChange,
}: {
  /** Prefixes the fields' ids: member or assistant. */
  id: string;
  /** Which engines are offered: those that may run a role, or an assistant. */
  use: EngineUse;
  value: ModelChoice;
  /** What is saved, which comes back on going back to its engine. */
  saved?: { engine: string; model?: string; provider?: string };
  onChange: (next: ModelChoice) => void;
}) {
  const { engine, model, effort } = value;
  const provider = value.provider ?? "";
  const choices = useEngineChoices();
  const choice = choiceFor(engine, choices);
  const listable = !!choice?.models;
  const api = choice?.cli === false;
  const providers = useProviders(api);
  const models = useModelCatalog(listable ? engine : "", api ? provider : "");
  const [typing, setTyping] = useState(false);
  const listed = models.options.some((option) => option.id === model);
  // A CLI always has its own default to fall back on; an API needs an id, so
  // it is typed in until its list offers some.
  const typed =
    typing || !listable || (!choice.cli && models.options.length === 0);
  const offered = engineOptions(choices, use, engine);
  // Roles show the engines they can't run on yet, and why.
  const unavailable =
    use === "roles" ? unavailableForRoles(choices, offered) : [];
  // Another engine's models don't run here; going back to the saved engine
  // brings back the saved model.
  const chooseEngine = (next: string) => {
    setTyping(false);
    const back = next === saved?.engine;
    onChange({
      ...value,
      engine: next,
      provider: back ? (saved.provider ?? "") : "",
      model: back ? (saved.model ?? "") : "",
    });
  };
  const chooseProvider = (next: string) => {
    const back =
      engine === saved?.engine && next === shownProvider(saved.provider);
    onChange({
      ...value,
      provider: savedProvider(next),
      model: back ? (saved.model ?? "") : "",
    });
  };
  const shown = shownProvider(provider);
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
            aria-describedby={
              unavailable.length ? `${id}-engine-unavailable` : undefined
            }
          >
            {offered.map((option) => (
              <option key={option.id} value={option.id}>
                {option.label}
              </option>
            ))}
            {unavailable.map((item) => (
              <option key={item.id} value={item.id} disabled>
                {item.label} (unavailable here)
              </option>
            ))}
          </select>
        </label>
        {api && providers.length > 1 && (
          <label htmlFor={`${id}-provider`}>
            Provider
            <select
              id={`${id}-provider`}
              className="field"
              value={shown}
              onChange={(e) => chooseProvider(e.target.value)}
            >
              {!providers.some((p) => p.id === shown) && (
                <option value={shown}>{shown} (saved)</option>
              )}
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </label>
        )}
        {!typed ? (
          <label htmlFor={`${id}-model`}>
            Model
            <select
              id={`${id}-model`}
              className="field"
              value={model}
              required={use === "roles" && api}
              onChange={(e) => onChange({ ...value, model: e.target.value })}
              disabled={models.loading}
            >
              <option value="">
                {choice?.cli ? "The engine's default" : "Choose a model"}
              </option>
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
              required={use === "roles" && api}
              maxLength={80}
              autoComplete="off"
              onChange={(e) => onChange({ ...value, model: e.target.value })}
            />
            <span className="hint">
              {typing
                ? "The model's id, as its engine or API names it."
                : models.error ||
                  models.catalog?.detail ||
                  "The model's id at the API."}
            </span>
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
      {unavailable.length > 0 && (
        <p className="hint" id={`${id}-engine-unavailable`}>
          {unavailable.map((item) => item.reason).join(" ")}
        </p>
      )}
      {listable && (
        <div className="actions">
          {typing ? (
            <button
              className="btn btn-sm"
              type="button"
              onClick={() => setTyping(false)}
            >
              Choose from the list
            </button>
          ) : (
            !typed && (
              <button
                className="btn btn-sm"
                type="button"
                onClick={() => setTyping(true)}
              >
                Enter a model id
              </button>
            )
          )}
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
