import { useEffect, useState } from "react";
import { api } from "./api";
import { engineLabel } from "./engines";

export type ModelOption = {
  id: string;
  name: string;
  description?: string;
  default_effort?: string;
  efforts: { id: string; description?: string; default?: boolean }[];
  /** False when the list doesn't say which efforts it takes; any may do. */
  efforts_known?: boolean;
  is_default: boolean;
  context_window?: number;
};
export type Catalog = {
  available: boolean;
  detail: string;
  engine: string;
  models: ModelOption[];
  current: { model: string; effort: string };
  default: { model: string; effort: string };
};

/**
 * The models an engine offers, as the assistant's own settings find them.
 * With no engine (one whose models can't be listed) there is nothing to list.
 */
export function useModelCatalog(engine: string) {
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
    api<Catalog>(
      `/api/models?profile=assistant&engine=${encodeURIComponent(engine)}`,
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
  }, [engine, revision]);
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

/** How a model is named in a list: marked if recommended or the default. */
export function modelLabel(option: ModelOption, catalog: Catalog | null) {
  if (option.id === catalog?.default.model)
    return `${option.name} (recommended)`;
  if (option.is_default)
    return `${option.name} (${engineLabel(catalog?.engine ?? "")} default)`;
  return option.name;
}

/** Whether a model's efforts are listed, so a pick can be offered from them. */
export const effortsListed = (option?: ModelOption) =>
  !!option && option.efforts_known !== false;
