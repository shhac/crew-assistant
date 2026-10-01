import { useEffect, useState, type FormEvent } from "react";
import { choiceFor, choicesFor, useEngineChoices } from "./engines";
import { ModelFields, type ModelChoice } from "./ModelFields";
import { PersonalityField, SuggestedLook } from "./ProfileFields";
import type { Suggestion } from "./SuggestIdentity";
import { ErrorNotice, useAction } from "./ui";
import {
  BrowserFields,
  savedBrowser,
  useBrowserOffered,
} from "./BrowserFields";
import { saveAssistant, type AssistantProfile, type Browser } from "./api";

const defaultMaxTokens = 4096;

/**
 * A new assistant or changes to one, laid out as a member's form is: who
 * they are, the model they run on, and how they write.
 */
export function AssistantForm({
  assistant,
  suggestion,
  onSaved,
  onCancel,
}: {
  assistant?: AssistantProfile;
  /** A suggestion the owner chose to fill in a new assistant with. */
  suggestion?: Suggestion;
  onSaved: (assistant: AssistantProfile) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(assistant?.name ?? "");
  const [picked, setChoice] = useState<ModelChoice>({
    engine: assistant?.model.engine ?? "",
    provider: assistant?.model.provider ?? "",
    model: assistant?.model.model ?? "",
    effort: assistant?.model.effort ?? "",
  });
  const [maxTokens, setMaxTokens] = useState(
    assistant?.model.max_tokens || defaultMaxTokens,
  );
  const [personality, setPersonality] = useState(assistant?.personality ?? "");
  const [browser, setBrowser] = useState<Browser>(assistant?.browser ?? {});
  useEffect(() => {
    if (!suggestion) return;
    setName(suggestion.name);
    setPersonality(suggestion.personality);
  }, [suggestion]);
  const choices = useEngineChoices();
  // A new assistant starts on the first engine that can run one.
  const choice = {
    ...picked,
    engine:
      picked.engine || (choicesFor(choices, "assistant")[0]?.engine ?? ""),
  };
  const api = choiceFor(choice.engine, choices)?.cli === false;
  const browserOffered = useBrowserOffered(choice.engine);
  const browserRefused = !!browser.on && !browserOffered;
  const { busy, error, run } = useAction();
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const saved = await saveAssistant(assistant?.id ?? "", {
        name: name.trim(),
        personality: personality.trim(),
        model: {
          engine: choice.engine,
          // Only a model on another API runs on a provider.
          ...(api && choice.provider ? { provider: choice.provider } : {}),
          model: choice.model.trim(),
          effort: choice.effort.trim(),
          max_tokens: maxTokens,
        },
        browser: savedBrowser(browser),
        ...(!assistant && suggestion ? { avatar: suggestion.avatar } : {}),
      });
      await onSaved(saved);
    });
  }
  return (
    <form
      className="form"
      onSubmit={save}
      aria-label={assistant ? `Edit ${assistant.name}` : "New assistant"}
    >
      <h2>{assistant ? `Edit ${assistant.name}` : "New assistant"}</h2>
      <label htmlFor="assistant-name">
        Name
        <input
          id="assistant-name"
          className="field"
          value={name}
          maxLength={80}
          onChange={(e) => setName(e.target.value)}
          required
        />
      </label>
      <ModelFields
        id="assistant"
        use="assistant"
        value={choice}
        saved={assistant?.model}
        onChange={setChoice}
      />
      {api && (
        <label htmlFor="assistant-max-tokens">
          Most output tokens per call
          <input
            type="number"
            id="assistant-max-tokens"
            className="field"
            min={128}
            max={131072}
            value={maxTokens}
            onChange={(e) => setMaxTokens(Number(e.target.value))}
          />
          <span className="hint">
            Where the API is and its key are in Settings, under Models.
          </span>
        </label>
      )}
      <BrowserFields
        id="assistant"
        engine={choice.engine}
        value={browser}
        onChange={setBrowser}
        purpose="assistant"
      />
      <PersonalityField
        id="assistant-personality"
        value={personality}
        onChange={setPersonality}
      />
      {!assistant && suggestion && <SuggestedLook suggestion={suggestion} />}
      <ErrorNotice error={error} />
      <div className="actions">
        <button
          className="btn btn-primary"
          type="submit"
          disabled={busy || !name.trim() || !choice.engine || browserRefused}
        >
          {assistant ? "Save" : "Add assistant"}
        </button>
        <button
          className="btn btn-quiet"
          type="button"
          disabled={busy}
          onClick={onCancel}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}
