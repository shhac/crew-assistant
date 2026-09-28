import type { EngineChoice } from "./api";

const none = {
  cli: false,
  assistant: false,
  roles: false,
  small: false,
  compact: false,
  usage: false,
  models: false,
  efforts: false,
  browser: false,
} satisfies Omit<EngineChoice, "engine" | "label">;

/**
 * Engine choices as a server offers them, for tests. Grok stands in for an
 * engine offered only for listing models, although the server now also
 * offers it for the assistant and small jobs.
 */
export const testChoices: EngineChoice[] = [
  {
    ...none,
    engine: "codex",
    label: "Codex",
    cli: true,
    assistant: true,
    roles: true,
    small: true,
    compact: true,
    usage: true,
    models: true,
    efforts: true,
  },
  {
    ...none,
    engine: "claude",
    label: "Claude",
    cli: true,
    assistant: true,
    roles: true,
    small: true,
    usage: true,
    models: true,
    efforts: true,
    browser: true,
  },
  {
    ...none,
    engine: "grok",
    label: "Grok",
    cli: true,
    models: true,
    efforts: true,
  },
  {
    ...none,
    engine: "openai-compatible",
    label: "Another API",
    assistant: true,
    small: true,
    models: true,
  },
];
