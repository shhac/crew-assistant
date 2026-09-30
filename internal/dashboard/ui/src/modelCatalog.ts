import { useEffect, useState } from "react";
import { api } from "./api";
import { engineLabel } from "./engines";

export type ModelOption = {
  /** What is saved: an alias here follows the engine's upgrades. */
  id: string;
  /** The concrete model an alias selects today, when the engine says. */
  resolved?: string;
  name: string;
  description?: string;
  default_effort?: string;
  efforts: { id: string; description?: string; default?: boolean }[];
  /** False when the list doesn't say which efforts it takes; any may do. */
  efforts_known?: boolean;
  is_default: boolean;
  context_window?: number;
  /** The endpoint names it free; such models have tight rate limits. */
  free?: boolean;
};
export type Catalog = {
  available: boolean;
  detail: string;
  engine: string;
  /** The API provider listed, for a model on another API. */
  provider?: string;
  models: ModelOption[];
  current: { model: string; effort: string };
  default: { model: string; effort: string };
};

/**
 * The models an engine offers, as the assistant's own settings find them;
 * on another API, those of the provider named, or of the first with none.
 * With no engine (one whose models can't be listed) there is nothing to list.
 */
export function useModelCatalog(engine: string, provider = "") {
  const [catalog, setCatalog] = useState<Catalog | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    if (!engine) {
      setCatalog(null);
      return;
    }
    let active = true;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    setCatalog(null);
    const on = provider ? `&provider=${encodeURIComponent(provider)}` : "";
    api<Catalog>(
      `/api/models?profile=assistant&engine=${encodeURIComponent(engine)}${on}`,
      {
        signal: controller.signal,
      },
    )
      .then((data) => {
        if (active) setCatalog(data);
      })
      .catch(() => {
        if (active)
          setError(
            "Couldn't load the list of models. Your choice hasn't changed.",
          );
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [engine, provider, revision]);
  const options =
    catalog?.available && catalog.engine === engine ? catalog.models : [];
  return {
    catalog,
    options,
    error,
    loading,
    refresh: () => setRevision((r) => r + 1),
  };
}

/**
 * How a model is named in a list: with the concrete model an alias selects
 * today, and marked if free, recommended or the default.
 */
export function modelLabel(option: ModelOption, catalog: Catalog | null) {
  const concrete = option.resolved;
  const named =
    concrete && concrete !== option.id && concrete !== option.name
      ? `${option.name} · ${concrete}`
      : option.name;
  // OpenRouter's free variants often say so in their name already.
  const name =
    option.free && !/\bfree\b/i.test(named) ? `${named} (free)` : named;
  if (option.id === catalog?.default.model) return `${name} (recommended)`;
  if (option.is_default)
    return `${name} (${engineLabel(catalog?.engine ?? "")} default)`;
  return name;
}

/** Whether a model's efforts are listed, so a pick can be offered from them. */
export const effortsListed = (option?: ModelOption) =>
  !!option && option.efforts_known !== false;
