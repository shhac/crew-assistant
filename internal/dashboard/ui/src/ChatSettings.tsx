import { href } from "./router";
import { section, type Config, type EngineChoice } from "./api";
import {
  choiceFor,
  engineLabel,
  smallLogins,
  useEngineChoices,
} from "./engines";

export function ChatSettings({
  config,
  onChange,
}: {
  config: Config;
  onChange: (value: Config) => void;
}) {
  const chat = section(config.chat);
  const phrases = section(chat.loading_phrases);
  const choices = useEngineChoices();
  const logins = smallLogins(choices);
  const seat = section(config.assistant).seat;
  const seated = config.assistants?.find((a) => a.id === seat);
  const engine = seated?.model.engine || logins[0]?.engine || "";
  const chosen = section(section(config.models).suggestions);
  const chosenEngine = String(chosen.engine ?? "");
  const enabled = phrases.enabled !== false;
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
        {smallModelsLine(choices, logins, engine, chosenEngine, chosen.model)}
        <a href={href({ page: "settings", section: "models" })}>
          Choose the model
        </a>
      </p>
    </div>
  );
}

/** Which model writes loading messages and suggestions, as a sentence. */
function smallModelsLine(
  choices: readonly EngineChoice[],
  logins: ReturnType<typeof smallLogins>,
  engine: string,
  chosenEngine: string,
  chosenModel: unknown,
) {
  if (chosenEngine && choiceFor(chosenEngine, choices)?.cli === false)
    return `Loading messages and next-message suggestions use ${String(chosenModel)} on your API, billed per call. No other model is used. `;
  if (chosenEngine)
    return `Loading messages and next-message suggestions use ${String(chosenModel)} on your ${engineLabel(chosenEngine, choices)} login. No other model is used. `;
  if (choiceFor(engine, choices)?.cli === false)
    return "The assistant uses an API, so loading messages are a fixed line and cost nothing extra. ";
  const own = logins.find((login) => login.engine === engine);
  if (!own) return "";
  const others = logins
    .filter((login) => login !== own)
    .map((login) => `your ${login.label} login (${login.model.detail})`);
  const fallback = others.length
    ? `, or ${others.join(", or ")} if that isn't working`
    : "";
  return `Loading messages and next-message suggestions use your ${own.label} login (${own.model.detail})${fallback}. No other model is used. `;
}
