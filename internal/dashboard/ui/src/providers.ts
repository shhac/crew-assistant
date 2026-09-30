import { useEffect, useState } from "react";
import { getProviders, legacyProvider, type ProviderChoice } from "./api";

/**
 * The saved API providers, the single API setting first, looked up only
 * while `load` says a model on another API is being chosen. Failing to
 * load them leaves the single API setting to choose, as before.
 */
export function useProviders(load: boolean): ProviderChoice[] {
  const [providers, setProviders] = useState<ProviderChoice[]>([]);
  useEffect(() => {
    if (!load) return;
    let active = true;
    getProviders().then(
      (list) => {
        if (active) setProviders(Array.isArray(list) ? list : []);
      },
      () => {},
    );
    return () => {
      active = false;
    };
  }, [load]);
  return providers;
}

/**
 * The provider a saved choice runs on as the picker shows it: none is the
 * single API setting.
 */
export const shownProvider = (provider?: string) => provider || legacyProvider;

/** What is saved for a provider picked: nothing for the single API setting. */
export const savedProvider = (id: string) => (id === legacyProvider ? "" : id);
