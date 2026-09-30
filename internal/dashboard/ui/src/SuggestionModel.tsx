import { useState } from "react";
import { section, type Config, type EngineChoice } from "./api";
import {
  choiceFor,
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
import { savedProvider, shownProvider, useProviders } from "./providers";

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
  const set = (next: Record<string, unknown>) => {
    const merged = { ...chosen, ...next };
    // No provider is the single API setting, and a CLI has none.
    if (!merged.provider) delete merged.provider;
    onChange({ ...config, models: { ...models, [group]: merged } });
  };
  const choices = useEngineChoices();
  const engine = value("engine");
  const choice = choiceFor(engine, choices);
  const listable = !!choice?.models;
  const api = choice?.cli === false;
  const providers = useProviders(api);
  const provider = api ? value("provider") : "";
  const { catalog, options, error, loading, refresh } = useModelCatalog(
    listable ? engine : "",
    provider,
  );
  const [typing, setTyping] = useState(false);
  // An API's model is typed in until its list offers some; a CLI keeps its
  // list, which offers the saved model while it can't be read. Any model can
  // be typed in instead.
  const typed = typing || !listable || (api && options.length === 0);
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
          onChange={(e) => {
            setTyping(false);
            set({
              engine: e.target.value,
              provider: "",
              model: "",
              effort: "",
            });
          }}
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
      {api && providers.length > 1 && (
        <label htmlFor={`${group}-provider`}>
          Provider
          <select
            id={`${group}-provider`}
            value={shownProvider(provider)}
            onChange={(e) =>
              set({ provider: savedProvider(e.target.value), model: "" })
            }
          >
            {!providers.some((p) => p.id === shownProvider(provider)) && (
              <option value={provider}>{provider} (saved)</option>
            )}
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </select>
          <span className="hint">
            Where the model is reached; each provider's address and key are
            under Engines.
          </span>
        </label>
      )}
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
            {api
              ? "The model's id at the API address under Engines. Each call is kept to a short reply and billed by the API."
              : "The model's id, as its login names it."}
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

function effortsFor(model: ModelOption, saved: string): string {
  if (!saved || model.efforts.some((effort) => effort.id === saved))
    return saved;
  return model.default_effort ?? "";
}
