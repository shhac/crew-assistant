import { section, type Config } from "./api";

// The only small models loading messages and suggestions may use. Haiku 4.5
// has no effort setting, so none is sent for it.
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
  const assistant = section(config.model);
  const engine = String(assistant.engine || "codex");
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
    <fieldset className="config-field-group">
      <legend>Conversation</legend>
      <label className="profile-choice">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(event) => change({ enabled: event.target.checked })}
          aria-describedby="loading-phrases-hint"
        />
        <span>Personalized loading phrases</span>
      </label>
      <p className="field-hint" id="loading-phrases-hint">
        A small model writes a brief loading message using only the last two
        messages, without tools. One extra model request per turn counts toward
        your shared model-call limit. If unavailable, a standard loading message
        appears.
      </p>
      {!local ? (
        <p className="field-hint">
          Your assistant uses an API provider. Loading messages use the standard
          fallback and make no additional model requests.
        </p>
      ) : (
        <p className="field-hint">
          Loading messages and next-message suggestions use your {own.label} CLI
          login ({own.detail}). If it is unavailable, they use your{" "}
          {other.label} login ({other.detail}) instead, and a CLI that just
          failed is left alone for a while. No other model is used.
        </p>
      )}
    </fieldset>
  );
}
