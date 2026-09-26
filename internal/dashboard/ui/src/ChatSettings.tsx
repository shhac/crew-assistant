import { href } from "./router";
import { section, type Config } from "./api";

// The small models loading messages and suggestions use unless the owner
// chooses another in Models. Haiku 4.5 has no effort setting, so none is
// sent for it.
const smallModels = {
  codex: { label: "Codex", detail: "gpt-6-luna, low effort" },
  claude: { label: "Claude", detail: "haiku" },
} as const;

export function ChatSettings({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const chat = section(config.chat);
  const phrases = section(chat.loading_phrases);
  const seat = section(config.assistant).seat;
  const seated = config.assistants?.find((a) => a.id === seat);
  const engine = seated?.model.engine || "codex";
  const chosen = section(section(config.models).suggestions);
  const chosenEngine = chosen.engine === "claude" ? "claude" : "codex";
  const local = engine === "codex" || engine === "claude";
  const enabled = phrases.enabled !== false;
  const own = smallModels[engine === "claude" ? "claude" : "codex"];
  const other = smallModels[engine === "claude" ? "codex" : "claude"];
  const change = (patch: Record<string, unknown>) =>
    onChange({
      ...config,
      chat: { ...chat, loading_phrases: { ...phrases, ...patch } },
    });

  return (
    <div className="form">
      <label className="check">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(event) => change({ enabled: event.target.checked })}
          aria-describedby="loading-phrases-hint"
        />
        <span>Show a line while it works</span>
      </label>
      <p className="hint" id="loading-phrases-hint">
        A small model writes it from the last two messages. It counts toward
        your daily model calls.
      </p>
      <p className="hint">
        {chosen.engine === "openai-compatible"
          ? `Loading messages and next-message suggestions use ${String(chosen.model)} on your API, billed per call. No other model is used. `
          : chosen.engine
            ? `Loading messages and next-message suggestions use ${String(chosen.model)} on your ${smallModels[chosenEngine].label} login. No other model is used. `
            : local
              ? `Loading messages and next-message suggestions use your ${own.label} login (${own.detail}), or your ${other.label} login (${other.detail}) if that isn't working. No other model is used. `
              : "The assistant uses an API, so loading messages are a fixed line and cost nothing extra. "}
        <a href={href({ page: "settings", section: "models" })}>
          Choose the model
        </a>
      </p>
    </div>
  );
}
