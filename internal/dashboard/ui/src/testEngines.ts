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
} satisfies Omit<EngineChoice, "engine" | "label">;

/** The engines as the server offers them today, for tests. */
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
