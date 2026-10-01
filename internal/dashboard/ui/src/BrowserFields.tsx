import { choicesFor, engineLabel, useEngineChoices } from "./engines";
import type { Browser } from "./api";

/** What the owner is told before anyone gets their browser. */
export const REAL_CHROME = "This is your real Chrome, with its logins.";

/** Whether a role on an engine can use the browser it ships. */
export function useBrowserOffered(engine: string) {
  return choicesFor(useEngineChoices(), "browser").some(
    (c) => c.engine === engine,
  );
}

/** How each engine's browser reaches Chrome, said where it is allowed. */
const engineBrowser: Record<string, string> = {
  claude: "On Claude, its browser tools act only inside Chrome.",
  codex:
    "On Codex, it runs through the ChatGPT app's browser bridge, which is checked before each session starts to be confined by its sandbox.",
};

/** What each holder of a browser setting is allowed it for. */
const purposes = {
  member: {
    label: "Allow browser use",
    hint: "Every turn this member takes can use it, whatever their roles, to look things up and read pages. Each turn is told it is only for controlling a browser, never to sign in, submit forms or act on an account, and to close its tabs.",
  },
  assistant: {
    label: "Allow browser use",
    hint: "The assistant can use it in your chat to look things up and read pages, and is told it is only for controlling a browser, never to sign in, submit forms or act on an account, and to close its tabs. While it is on, the chat can also read files on this machine, though never change them, and its shell reaches no network.",
  },
  qa: {
    label: "QA uses the browser to try the app",
    hint: "QA opens only the app, on this machine, in tabs of its own, and closes them when it is done.",
  },
};

/**
 * A browser setting: off, or on with the name of a connected browser, empty
 * for the one the extension connects by default. A member's allows the
 * browser in every turn they take; the assistant's, in the owner's chat;
 * QA's lets it try the app. It is offered
 * only on an engine whose roles can use one; one left on elsewhere can only
 * be switched off.
 */
export function BrowserFields({
  id,
  engine,
  value,
  onChange,
  purpose = "qa",
}: {
  id: string;
  engine: string;
  value: Browser;
  onChange: (b: Browser) => void;
  purpose?: keyof typeof purposes;
}) {
  const offered = useBrowserOffered(engine);
  if (!offered && !value.on) return null;
  return (
    <fieldset className="browser-setting">
      <legend>Browser</legend>
      <label className="check">
        <input
          type="checkbox"
          checked={!!value.on}
          onChange={(e) => onChange({ ...value, on: e.target.checked })}
        />
        <span>{purposes[purpose].label}</span>
      </label>
      {offered ? (
        <p className="hint">
          <strong>{REAL_CHROME}</strong> {purposes[purpose].hint}{" "}
          {engineBrowser[engine] ?? ""}
        </p>
      ) : (
        <p className="member-kinds-problem">
          {engineLabel(engine)} can't use the browser. Switch it off to save.
        </p>
      )}
      {value.on && offered && (
        <label htmlFor={`${id}-browser-name`}>
          Connected browser
          <input
            id={`${id}-browser-name`}
            className="field"
            value={value.name ?? ""}
            maxLength={100}
            placeholder="The one the extension connects by default"
            onChange={(e) => onChange({ ...value, name: e.target.value })}
          />
          <span className="hint">
            Optional. Leave it empty unless more than one browser is connected.
          </span>
        </label>
      )}
    </fieldset>
  );
}

/** A browser setting as it is saved: on or off, its name trimmed. */
export const savedBrowser = (b: Browser): Browser => ({
  on: !!b.on,
  name: (b.name ?? "").trim(),
});
