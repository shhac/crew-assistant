import { useState, type FormEvent } from "react";
import { choicesFor, engineLabel, useEngineChoices } from "./engines";
import { seatFor } from "./members";
import { ErrorNotice, useAction } from "./ui";
import { setQABrowser, type Browser, type Playbook, type Project } from "./api";

/** What the owner is told before QA gets their browser. */
export const REAL_CHROME = "This is your real Chrome, with its logins.";

/** Whether QA on an engine can use the browser it ships. */
export function useBrowserOffered(engine: string) {
  return choicesFor(useEngineChoices(), "browser").some(
    (c) => c.engine === engine,
  );
}

/**
 * QA's browser setting: off, or on with the name of a connected browser,
 * empty for the one the extension connects by default. It is offered only
 * on an engine that has a browser; one left on elsewhere can only be
 * switched off.
 */
export function BrowserFields({
  id,
  engine,
  value,
  onChange,
}: {
  id: string;
  engine: string;
  value: Browser;
  onChange: (b: Browser) => void;
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
        <span>QA uses the browser to try the app</span>
      </label>
      {offered ? (
        <p className="hint">
          <strong>{REAL_CHROME}</strong> QA opens only the app, on this machine,
          in tabs of its own, and closes them when it is done.
        </p>
      ) : (
        <p className="member-kinds-problem">
          QA on {engineLabel(engine)} can't use the browser. Switch it off to
          save.
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

/** Whether this project's QA uses the browser, which the owner can change. */
export function QABrowser({
  project,
  playbook,
  refresh,
}: {
  project: Project;
  playbook: Playbook;
  refresh: () => Promise<void>;
}) {
  const qa = seatFor(playbook, "qa");
  const offered = useBrowserOffered(qa?.engine ?? "");
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState<Browser>(qa?.browser ?? {});
  const { busy, error, run } = useAction();
  if (!qa || (!offered && !qa.browser?.on)) return null;
  async function save(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      await setQABrowser(project.id, {
        on: !!value.on,
        name: (value.name ?? "").trim(),
      });
      await refresh();
      setEditing(false);
    });
  }
  if (editing)
    return (
      <form className="form section" aria-label="QA's browser" onSubmit={save}>
        <BrowserFields
          id="seat"
          engine={qa.engine}
          value={value}
          onChange={setValue}
        />
        <ErrorNotice error={error} />
        <div className="actions">
          <button className="btn btn-primary" type="submit" disabled={busy}>
            Save
          </button>
          <button
            className="btn btn-quiet"
            type="button"
            disabled={busy}
            onClick={() => setEditing(false)}
          >
            Cancel
          </button>
        </div>
      </form>
    );
  const on = qa.browser?.on;
  return (
    <div className="section" aria-label="QA's browser">
      <div className="panel-head">
        <p className="label">QA's browser</p>
        <button
          type="button"
          className="btn btn-sm"
          onClick={() => {
            setValue(qa.browser ?? {});
            setEditing(true);
          }}
        >
          Edit
        </button>
      </div>
      <p className={on ? "" : "muted"}>
        {on
          ? `On, ${qa.browser?.name ? `using the connected browser “${qa.browser.name}”` : "using the browser the extension connects by default"}. ${REAL_CHROME}`
          : "Off. QA doesn't use a browser."}
      </p>
      <p className="hint">
        Used only when the project has a run recipe, to try the app.
      </p>
    </div>
  );
}
