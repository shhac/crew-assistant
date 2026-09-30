import { useEffect, useSyncExternalStore } from "react";
import {
  getConfigDefaults,
  type ConfigDefaults,
  type EngineChoice,
} from "./api";

/** What an engine may be used for, as its choice says. */
export type EngineUse = Exclude<
  keyof EngineChoice,
  "engine" | "label" | "roles_reason"
>;

/**
 * The small model each CLI login uses for suggestions and loading lines
 * unless the owner picks another. Haiku 4.5 has no effort setting, so none is
 * sent for it.
 */
export const smallModels: Record<string, { name: string; detail: string }> = {
  codex: { name: "Luna", detail: "gpt-6-luna, low effort" },
  claude: { name: "Haiku", detail: "haiku" },
};

let known: readonly EngineChoice[] = [];
let pending: Promise<ConfigDefaults> | undefined;
const listeners = new Set<() => void>();

/** Keeps the engines the server offers, for every list and label to use. */
export function rememberChoices(choices?: readonly EngineChoice[]) {
  if (!Array.isArray(choices) || choices.length === 0) return;
  known = choices;
  listeners.forEach((listener) => listener());
}

/**
 * The defaults, remembering the engine choices they carry. A request already
 * under way is shared rather than sent again.
 */
export function loadDefaults(): Promise<ConfigDefaults> {
  pending ??= getConfigDefaults()
    .then((defaults) => {
      rememberChoices(defaults?.choices);
      return defaults;
    })
    .finally(() => {
      pending = undefined;
    });
  return pending;
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/**
 * The engines the server offers, in its order; empty until they have loaded.
 * Loading them fails quietly: lists wait and labels fall back to the id.
 * `load` holds off asking until the session can.
 */
export function useEngineChoices(load = true): readonly EngineChoice[] {
  const choices = useSyncExternalStore(subscribe, () => known);
  useEffect(() => {
    if (load && known.length === 0) loadDefaults().catch(() => {});
  }, [load]);
  return choices;
}

/** The engines offered for a use, such as running a team role. */
export const choicesFor = (choices: readonly EngineChoice[], use: EngineUse) =>
  choices.filter((choice) => choice[use]);

export const choiceFor = (engine: string, choices = known) =>
  choices.find((choice) => choice.engine === engine);

/** "Claude"; an engine not loaded yet reads as its id, capitalized. */
export function engineLabel(engine: string, choices = known) {
  const label = choiceFor(engine, choices)?.label;
  if (label) return label;
  return engine.charAt(0).toUpperCase() + engine.slice(1);
}

/**
 * The CLI logins suggestions and loading lines fall back on, with the small
 * model each uses, in the server's order.
 */
export const smallLogins = (choices: readonly EngineChoice[]) =>
  choicesFor(choices, "small").flatMap((choice) => {
    const model = smallModels[choice.engine];
    return choice.cli && model ? [{ ...choice, model }] : [];
  });

/**
 * The engines that can't run team roles for a reason the server gives, such
 * as an API provider without a sandbox, to show beside the ones that can;
 * one already among `offered`, such as a saved choice, isn't listed twice.
 * Every role engine picker shows them, disabled, with the reason.
 */
export const unavailableForRoles = (
  choices: readonly EngineChoice[],
  offered: readonly { id: string }[] = [],
) =>
  choices.flatMap((choice) =>
    !choice.roles &&
    choice.roles_reason &&
    !offered.some((option) => option.id === choice.engine)
      ? [
          {
            id: choice.engine,
            label: choice.label,
            reason: choice.roles_reason,
          },
        ]
      : [],
  );

/**
 * An engine's choices as select options, keeping one already chosen that
 * isn't offered for this use, so a saved choice is never silently changed.
 */
export function engineOptions(
  choices: readonly EngineChoice[],
  use: EngineUse,
  current = "",
) {
  const offered = choicesFor(choices, use).map((choice) => ({
    id: choice.engine,
    label: choice.label,
  }));
  if (!current || offered.some((option) => option.id === current))
    return offered;
  return [...offered, { id: current, label: engineLabel(current, choices) }];
}
